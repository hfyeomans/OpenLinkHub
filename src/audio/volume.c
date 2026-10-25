/*
 * volume.c
 *
 * PipeWire system-volume control binding.
 *
 * Maintains an INDEPENDENT PipeWire connection (own pw_thread_loop,
 * pw_context, pw_core and registry) that never touches the virtual-sink
 * DSP engine in audio.c: no shared globals, no shared stream, no shared
 * connection. audio.c calls pw_init()/pw_deinit() around engine
 * start/stop; pw_init() is refcounted, so this module's own pw_init()
 * reference keeps libpipewire usable for the daemon lifetime.
 *
 * The registry listener tracks every node with media.class = Audio/Sink
 * (same pattern as audio.c registry_global) and binds each one to
 * observe its Props (volume / mute / channelVolumes). The default
 * output sink is resolved from the "default" Metadata object's
 * default.audio.sink entry (SPA JSON: {"name":"<node.name>"}); when the
 * metadata is missing or names an unknown node, the first non-virtual
 * Audio/Sink is used as fallback.
 *
 * Volume semantics (verified against PipeWire 1.6.x sources):
 *  - PipeWire volume is LINEAR 0..1 (1.0 = no attenuation, 0.0 =
 *    silence). The UI maps 0-100 linearly onto this range. This
 *    diverges from wpctl's cubic display scale, but the value reported
 *    by `wpctl get-volume` is the same underlying linear value this
 *    module returns (channelVolumes[0], WirePlumber mixer-api
 *    convention).
 *  - The adapter applies the master `volume` prop as a MULTIPLIER over
 *    the per-channel `channelVolumes` (channelmix: volumes[i] =
 *    channel_volumes[i] * volume). Writes therefore follow the session
 *    manager's convention (WirePlumber mixer-api): set `channelVolumes`
 *    (scaled proportionally to preserve balance) and `mute`, and never
 *    the master `volume` prop together with channelVolumes (that would
 *    apply the volume twice).
 *
 * All exported functions are defensive: argument/NULL checks, bounded
 * waits, and error returns instead of crashes. The Go wrappers
 * serialize calls with the audio package mutex; the C module itself is
 * lock-safe but not re-entrant across its own exported calls.
 */

#include "audio.h"

#include <pipewire/pipewire.h>
#include <pipewire/thread-loop.h>
#include <pipewire/keys.h>
#include <pipewire/extensions/metadata.h>

#include <spa/param/param.h>
#include <spa/param/props.h>
#include <spa/pod/builder.h>
#include <spa/pod/iter.h>
#include <spa/utils/type.h>
#include <spa/utils/json.h>
#include <spa/utils/result.h>

#include <pthread.h>
#include <stdatomic.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>
#include <stdarg.h>

#define VOL_MAX_SINKS       64
#define VOL_MAX_CHANNELS    64
#define VOL_NAME_LEN        192
#define VOL_DESC_LEN        256
#define VOL_ERROR_LEN       256

/* Bounded initial sync: wait for the registry dump + initial Props. */
#define VOL_SYNC_ITERS      200   /* 200 x 10 ms = 2 s worst case */
#define VOL_SYNC_SLEEP_MS   10

/* Do not hammer a missing/unavailable PipeWire daemon: minimum delay
 * between reconnect attempts. */
#define VOL_RETRY_DELAY_MS  5000

/* Must match DEVICE_SINK_DESC.name in audio.c. */
#define VOL_VIRTUAL_SINK_NAME "openlinkhub-virtual-device"

/* Error codes returned by the audio_system_* functions. */
#define VOL_OK              0
#define VOL_ERR_NO_SINK    -1
#define VOL_ERR_CONNECT    -2
#define VOL_ERR_PARAM      -3

typedef struct {
	uint32_t id;                 /* registry global id */
	int used;                    /* slot occupied; removed sinks leave holes */
	char name[VOL_NAME_LEN];
	char desc[VOL_DESC_LEN];
	struct pw_proxy *proxy;      /* bound node proxy */
	struct spa_hook node_listener;

	/* last observed Props, updated by param events */
	float volume;                /* master volume prop (linear 0..1) */
	float chv[VOL_MAX_CHANNELS]; /* per-channel linear volumes */
	uint32_t n_chv;
	int muted;
	int have_volume;
	int have_mute;
} vol_sink_t;

static struct {
	pthread_mutex_t mu;          /* guards sinks[] and default_name */
	pthread_mutex_t error_mu;    /* guards last_error across threads */

	struct pw_thread_loop *loop;
	struct pw_context *ctx;
	struct pw_core *core;
	struct pw_registry *reg;
	struct spa_hook core_listener;
	struct spa_hook reg_listener;

	struct pw_proxy *metadata;   /* "default" metadata proxy */
	struct spa_hook metadata_listener;
	uint32_t metadata_id;
	char default_name[VOL_NAME_LEN];

	vol_sink_t sinks[VOL_MAX_SINKS];
	int n_sinks;
	int n_used;                  /* occupied slots; n_sinks is the high-water mark */

	atomic_int connected;
	atomic_int failed;
	atomic_uint sync_seq;
	atomic_int sync_done;
	atomic_llong last_fail_ms;

	int pw_inited;
	int loop_started;
	char last_error[VOL_ERROR_LEN];
} vol = {
	.mu = PTHREAD_MUTEX_INITIALIZER,
	.error_mu = PTHREAD_MUTEX_INITIALIZER,
};

static void vol_set_error(const char *fmt, ...)
{
	char buf[VOL_ERROR_LEN];
	va_list ap;

	va_start(ap, fmt);
	vsnprintf(buf, sizeof(buf), fmt, ap);
	va_end(ap);

	/* Callbacks and Go callers run on different threads. */
	pthread_mutex_lock(&vol.error_mu);
	snprintf(vol.last_error, sizeof(vol.last_error), "%s", buf);
	pthread_mutex_unlock(&vol.error_mu);
}

static int64_t vol_now_ms(void)
{
	struct timespec ts;
	clock_gettime(CLOCK_MONOTONIC, &ts);
	return (int64_t)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}

/* Linear clamp that also maps NaN to 0. */
static float vol_clamp01(float v)
{
	if (!(v >= 0.f))
	{
		return 0.f;
	}

	if (v > 1.f)
	{
		return 1.f;
	}

	return v;
}

/*
 * Parses the default.audio.sink metadata value. The canonical form is a
 * SPA JSON object: {"name":"<node.name>"}. A bare quoted string is
 * accepted defensively. Anything unparsable yields an empty string.
 */
static void vol_parse_default_name(const char *value, char *out, size_t outlen)
{
	struct spa_json it, sub;
	char key[64];
	const char *val;
	int len;

	out[0] = '\0';
	if (!value || !value[0] || outlen == 0)
	{
		return;
	}

	len = (int)strlen(value);

	if (value[0] == '"')
	{
		if (spa_json_parse_stringn(value, len, out, (int)outlen) <= 0)
		{
			out[0] = '\0';
		}
		return;
	}

	spa_json_init(&it, value, len);
	if (spa_json_enter_object(&it, &sub) <= 0)
	{
		return;
	}

	while (spa_json_get_string(&sub, key, sizeof(key)) > 0)
	{
		int vlen = spa_json_next(&sub, &val);
		if (vlen <= 0)
		{
			break;
		}

		if (strcmp(key, "name") == 0 && out[0] == '\0')
		{
			if (spa_json_parse_stringn(val, vlen, out, (int)outlen) <= 0)
			{
				out[0] = '\0';
			}
		}
	}
}

static vol_sink_t *vol_find_by_id_locked(uint32_t id)
{
	for (int i = 0; i < vol.n_sinks; i++)
	{
		if (vol.sinks[i].used && vol.sinks[i].id == id)
		{
			return &vol.sinks[i];
		}
	}

	return NULL;
}

/*
 * Resolves the sink to control. Caller holds vol.mu.
 *  1. metadata default.audio.sink name match — even when it is our own
 *     virtual sink: if the user made it the default output, it IS the
 *     system default and controlling it is correct (this is also what
 *     wpctl targets),
 *  2. first Audio/Sink that is not the engine's virtual sink,
 *  3. first Audio/Sink.
 */
static vol_sink_t *vol_resolve_default_locked(void)
{
	if (vol.default_name[0])
	{
		for (int i = 0; i < vol.n_sinks; i++)
		{
			if (vol.sinks[i].used &&
			    strcmp(vol.sinks[i].name, vol.default_name) == 0)
			{
				return &vol.sinks[i];
			}
		}
	}

	int first_any = -1;
	int first_real = -1;
	for (int i = 0; i < vol.n_sinks; i++)
	{
		if (!vol.sinks[i].used)
		{
			continue;
		}
		if (first_any < 0)
		{
			first_any = i;
		}

		if (first_real < 0 &&
		    strcmp(vol.sinks[i].name, VOL_VIRTUAL_SINK_NAME) != 0)
		{
			first_real = i;
		}
	}

	if (first_real >= 0)
	{
		return &vol.sinks[first_real];
	}

	if (first_any >= 0)
	{
		return &vol.sinks[first_any];
	}

	return NULL;
}

/*
 * Props updates arrive on the thread loop. vol.mu is taken INSIDE the
 * thread loop lock; the exported functions use the same order
 * (thread loop lock, then vol.mu), so no inversion is possible.
 */
static void vol_node_param(
	void *data,
	int seq,
	uint32_t id,
	uint32_t index,
	uint32_t next,
	const struct spa_pod *param)
{
	vol_sink_t *s = data;
	const struct spa_pod_prop *p;

	(void)seq;
	(void)index;
	(void)next;

	if (!s || id != SPA_PARAM_Props || !param || !spa_pod_is_object(param))
	{
		return;
	}

	if (SPA_POD_OBJECT_TYPE(param) != SPA_TYPE_OBJECT_Props)
	{
		return;
	}

	pthread_mutex_lock(&vol.mu);

	p = spa_pod_object_find_prop(
		(const struct spa_pod_object *)param, NULL, SPA_PROP_volume);
	if (p && spa_pod_is_float(&p->value))
	{
		float v = 0.f;
		if (spa_pod_get_float(&p->value, &v) == 0)
		{
			s->volume = v;
			s->have_volume = 1;
		}
	}

	p = spa_pod_object_find_prop(
		(const struct spa_pod_object *)param, NULL, SPA_PROP_mute);
	if (p && spa_pod_is_bool(&p->value))
	{
		bool m = false;
		if (spa_pod_get_bool(&p->value, &m) == 0)
		{
			s->muted = m ? 1 : 0;
			s->have_mute = 1;
		}
	}

	p = spa_pod_object_find_prop(
		(const struct spa_pod_object *)param, NULL, SPA_PROP_channelVolumes);
	if (p && spa_pod_is_array(&p->value) &&
	    SPA_POD_ARRAY_VALUE_TYPE(&p->value) == SPA_TYPE_Float)
	{
		uint32_t n = 0;
		const float *arr = (const float *)spa_pod_get_array(&p->value, &n);

		if (arr && n > 0)
		{
			if (n > VOL_MAX_CHANNELS)
			{
				n = VOL_MAX_CHANNELS;
			}

			memcpy(s->chv, arr, (size_t)n * sizeof(float));
			s->n_chv = n;
		}
		else
		{
			s->n_chv = 0;
		}
	}

	pthread_mutex_unlock(&vol.mu);
}

static const struct pw_node_events vol_node_events = {
	PW_VERSION_NODE_EVENTS,
	.param = vol_node_param,
};

/* Fires for every property of the "default" metadata (also right after
 * bind, when the server dumps the existing entries). */
static int vol_metadata_property(
	void *data,
	uint32_t subject,
	const char *key,
	const char *type,
	const char *value)
{
	char name[VOL_NAME_LEN];

	(void)data;
	(void)subject;
	(void)type;

	if (!key || strcmp(key, "default.audio.sink") != 0)
	{
		return 0;
	}

	vol_parse_default_name(value, name, sizeof(name));

	pthread_mutex_lock(&vol.mu);
	snprintf(vol.default_name, sizeof(vol.default_name), "%s", name);
	pthread_mutex_unlock(&vol.mu);
	return 0;
}

static const struct pw_metadata_events vol_metadata_events = {
	PW_VERSION_METADATA_EVENTS,
	.property = vol_metadata_property,
};

static void vol_core_done(void *data, uint32_t id, int seq)
{
	(void)data;

	if (id == PW_ID_CORE && (uint32_t)seq == atomic_load(&vol.sync_seq))
	{
		atomic_store(&vol.sync_done, 1);
	}
}

static void vol_core_error(
	void *data,
	uint32_t id,
	int seq,
	int res,
	const char *message)
{
	(void)data;
	(void)id;
	(void)seq;

	vol_set_error(
		"PipeWire core error: %s (%d)",
		message ? message : "unknown",
		res
	);
	atomic_store(&vol.failed, 1);
}

static const struct pw_core_events vol_core_events = {
	PW_VERSION_CORE_EVENTS,
	.done = vol_core_done,
	.error = vol_core_error,
};

/* Called on the thread loop with its lock held. */
static void vol_registry_global(
	void *data,
	uint32_t id,
	uint32_t permissions,
	const char *type,
	uint32_t version,
	const struct spa_dict *props)
{
	(void)data;
	(void)permissions;
	(void)version;

	if (!props || !type)
	{
		return;
	}

	/* Track the "default" metadata object for default-sink resolution. */
	if (strcmp(type, PW_TYPE_INTERFACE_Metadata) == 0)
	{
		const char *name = spa_dict_lookup(props, PW_KEY_METADATA_NAME);
		if (name && strcmp(name, "default") == 0 && vol.metadata == NULL)
		{
			vol.metadata_id = id;
			vol.metadata = pw_registry_bind(
				vol.reg,
				id,
				PW_TYPE_INTERFACE_Metadata,
				PW_VERSION_METADATA,
				0
			);

			if (vol.metadata)
			{
				pw_metadata_add_listener(
					(struct pw_metadata *)vol.metadata,
					&vol.metadata_listener,
					&vol_metadata_events,
					NULL
				);
			}
			else
			{
				vol.metadata_id = 0;
				vol_set_error("failed to bind default metadata");
			}
		}
		return;
	}

	if (strcmp(type, PW_TYPE_INTERFACE_Node) != 0)
	{
		return;
	}

	/* Same pattern as audio.c registry_global: media.class = Audio/Sink. */
	const char *media_class = spa_dict_lookup(props, PW_KEY_MEDIA_CLASS);
	if (!media_class || strcmp(media_class, "Audio/Sink") != 0)
	{
		return;
	}

	const char *name = spa_dict_lookup(props, PW_KEY_NODE_NAME);
	const char *desc = spa_dict_lookup(props, PW_KEY_NODE_DESCRIPTION);

	pthread_mutex_lock(&vol.mu);

	if (vol_find_by_id_locked(id) != NULL || vol.n_used >= VOL_MAX_SINKS)
	{
		pthread_mutex_unlock(&vol.mu);
		return;
	}

	/* Reuse a tombstoned slot so live spa_hooks never move. */
	vol_sink_t *s = NULL;
	for (int i = 0; i < vol.n_sinks; i++)
	{
		if (!vol.sinks[i].used)
		{
			s = &vol.sinks[i];
			break;
		}
	}
	if (s == NULL)
	{
		s = &vol.sinks[vol.n_sinks];
		vol.n_sinks++;
	}
	memset(s, 0, sizeof(*s));
	s->id = id;
	snprintf(s->name, sizeof(s->name), "%s", name ? name : "");
	snprintf(s->desc, sizeof(s->desc), "%s", desc ? desc : "");

	s->proxy = pw_registry_bind(
		vol.reg,
		id,
		PW_TYPE_INTERFACE_Node,
		PW_VERSION_NODE,
		0
	);

	if (!s->proxy)
	{
		vol_set_error("failed to bind sink node id=%u", id);
		pthread_mutex_unlock(&vol.mu);
		return;
	}

	pw_node_add_listener(
		(struct pw_node *)s->proxy,
		&s->node_listener,
		&vol_node_events,
		s
	);

	/* Fetch the current Props and subscribe to future changes. */
	uint32_t ids[1] = { SPA_PARAM_Props };
	pw_node_subscribe_params((struct pw_node *)s->proxy, ids, 1);
	pw_node_enum_params(
		(struct pw_node *)s->proxy, 0, SPA_PARAM_Props, 0, 0, NULL);

	pthread_mutex_unlock(&vol.mu);
}

/* Called on the thread loop with its lock held. */
static void vol_registry_global_remove(void *data, uint32_t id)
{
	(void)data;

	pthread_mutex_lock(&vol.mu);

	if (vol.metadata != NULL && id == vol.metadata_id)
	{
		spa_hook_remove(&vol.metadata_listener);
		pw_proxy_destroy(vol.metadata);
		vol.metadata = NULL;
		vol.metadata_id = 0;
		vol.default_name[0] = '\0';
	}
	else
	{
		int n = vol.n_sinks;
		for (int i = 0; i < n; i++)
		{
			if (!vol.sinks[i].used || vol.sinks[i].id != id)
			{
				continue;
			}

			/* Detach the hook BEFORE moving the struct: the hook
			 * is linked into the proxy's list and must never be
			 * moved or copied. Leave a tombstone hole instead of
			 * compacting: a moved struct would leave the proxy's
			 * listener list pointing at zeroed memory. */
			spa_hook_remove(&vol.sinks[i].node_listener);
			if (vol.sinks[i].proxy)
			{
				pw_proxy_destroy(vol.sinks[i].proxy);
			}
			memset(&vol.sinks[i], 0, sizeof(vol_sink_t));
			vol.n_used--;
			break;
		}
	}

	pthread_mutex_unlock(&vol.mu);
}

static const struct pw_registry_events vol_registry_events = {
	PW_VERSION_REGISTRY_EVENTS,
	.global = vol_registry_global,
	.global_remove = vol_registry_global_remove,
};

static void vol_teardown(void)
{
	struct pw_thread_loop *loop = vol.loop;

	atomic_store(&vol.connected, 0);

	if (loop != NULL)
	{
		pw_thread_loop_lock(loop);

		pthread_mutex_lock(&vol.mu);
		for (int i = 0; i < vol.n_sinks; i++)
		{
			if (vol.sinks[i].proxy != NULL)
			{
				spa_hook_remove(&vol.sinks[i].node_listener);
				pw_proxy_destroy(vol.sinks[i].proxy);
			}
		}
		memset(vol.sinks, 0, sizeof(vol.sinks));
		vol.n_sinks = 0;

		if (vol.metadata != NULL)
		{
			spa_hook_remove(&vol.metadata_listener);
			pw_proxy_destroy(vol.metadata);
			vol.metadata = NULL;
		}
		vol.metadata_id = 0;
		vol.default_name[0] = '\0';
		pthread_mutex_unlock(&vol.mu);

		if (vol.reg != NULL)
		{
			pw_proxy_destroy((struct pw_proxy *)vol.reg);
			vol.reg = NULL;
		}

		if (vol.core != NULL)
		{
			pw_core_disconnect(vol.core);
			vol.core = NULL;
		}

		pw_thread_loop_unlock(loop);
		if (vol.loop_started)
		{
			pw_thread_loop_stop(loop);
		}
		vol.loop_started = 0;
	}

	if (vol.ctx != NULL)
	{
		pw_context_destroy(vol.ctx);
		vol.ctx = NULL;
	}

	if (vol.loop != NULL)
	{
		pw_thread_loop_destroy(vol.loop);
		vol.loop = NULL;
	}

	atomic_store(&vol.sync_seq, 0);
	atomic_store(&vol.sync_done, 0);
	atomic_store(&vol.failed, 0);
}

static int vol_connect(void)
{
	if (!vol.pw_inited)
	{
		pw_init(NULL, NULL);
		vol.pw_inited = 1;
	}

	vol.loop = pw_thread_loop_new("OpenLinkHub-volume", NULL);
	if (vol.loop == NULL)
	{
		vol_set_error("failed to create PipeWire thread loop");
		return -1;
	}

	vol.ctx = pw_context_new(pw_thread_loop_get_loop(vol.loop), NULL, 0);
	if (vol.ctx == NULL)
	{
		vol_set_error("failed to create PipeWire context");
		return -1;
	}

	if (pw_thread_loop_start(vol.loop) < 0)
	{
		vol_set_error("failed to start PipeWire thread loop");
		return -1;
	}
	vol.loop_started = 1;

	pw_thread_loop_lock(vol.loop);

	vol.core = pw_context_connect(vol.ctx, NULL, 0);
	if (vol.core == NULL)
	{
		pw_thread_loop_unlock(vol.loop);
		vol_set_error("failed to connect to PipeWire");
		return -1;
	}

	pw_core_add_listener(vol.core, &vol.core_listener, &vol_core_events, NULL);

	vol.reg = pw_core_get_registry(vol.core, PW_VERSION_REGISTRY, 0);
	if (vol.reg == NULL)
	{
		pw_thread_loop_unlock(vol.loop);
		vol_set_error("failed to get PipeWire registry");
		return -1;
	}

	pw_registry_add_listener(
		vol.reg, &vol.reg_listener, &vol_registry_events, NULL);

	/* Bounded wait for the initial registry dump. */
	atomic_store(&vol.sync_done, 0);
	int seq = pw_core_sync(vol.core, PW_ID_CORE, 0);
	atomic_store(&vol.sync_seq, (uint32_t)seq);
	pw_thread_loop_unlock(vol.loop);

	for (int i = 0; i < VOL_SYNC_ITERS && !atomic_load(&vol.sync_done); i++)
	{
		usleep(VOL_SYNC_SLEEP_MS * 1000);
	}

/* A timed-out registry dump is a connect failure: the caller must
 * throttle instead of stalling every poll for 2 s. */
	if (!atomic_load(&vol.sync_done))
	{
		vol_set_error("timed out waiting for initial PipeWire registry dump");
		return VOL_ERR_CONNECT;
	}
	return 0;
}

/*
 * Lazily connects (or reconnects after a core error); retry-throttled
 * after failures. Returns VOL_OK when the connection is ready. Only one
 * goroutine may call the audio_system_* functions at a time (the Go
 * wrappers serialize with the audio package mutex).
 */
static int vol_ensure_connected(void)
{
	if (atomic_load(&vol.connected) && !atomic_load(&vol.failed))
	{
		return VOL_OK;
	}

	int64_t now = vol_now_ms();
	int64_t last = atomic_load(&vol.last_fail_ms);
	if (last > 0 && now - last < VOL_RETRY_DELAY_MS)
	{
		vol_set_error("PipeWire unavailable (retrying later)");
		return VOL_ERR_CONNECT;
	}

	if (atomic_load(&vol.connected))
	{
		vol_teardown(); /* stale connection: rebuild */
	}

	if (vol_connect() != 0)
	{
		vol_teardown();
		atomic_store(&vol.last_fail_ms, vol_now_ms());
		return VOL_ERR_CONNECT;
	}

	atomic_store(&vol.connected, 1);
	atomic_store(&vol.failed, 0);
	atomic_store(&vol.last_fail_ms, 0);
	return VOL_OK;
}

int audio_system_get_volume(float *volume, int *muted)
{
	int res = VOL_OK;

	res = vol_ensure_connected();
	if (res != VOL_OK)
	{
		return res;
	}

	if (vol.loop == NULL)
	{
		return VOL_ERR_CONNECT;
	}

	pw_thread_loop_lock(vol.loop);
	pthread_mutex_lock(&vol.mu);

	vol_sink_t *s = vol_resolve_default_locked();
	if (s == NULL)
	{
		vol_set_error("no audio sink available");
		res = VOL_ERR_NO_SINK;
	}
	else
	{
		float v = 0.f;
		int m = 0;

		if (s->n_chv > 0)
		{
			/* WirePlumber/wpctl convention: the scalar volume is
			 * channelVolumes[0]; the master `volume` prop stays at 1.0. */
			v = vol_clamp01(s->chv[0]);
		}
		else if (s->have_volume)
		{
			v = vol_clamp01(s->volume);
		}
		else
		{
			vol_set_error("volume props not available for sink '%s'", s->name);
			res = VOL_ERR_NO_SINK;
		}

		if (res == VOL_OK)
		{
			m = s->have_mute ? (s->muted ? 1 : 0) : 0;
			if (volume != NULL)
			{
				*volume = v;
			}
			if (muted != NULL)
			{
				*muted = m;
			}
		}
	}

	pthread_mutex_unlock(&vol.mu);
	pw_thread_loop_unlock(vol.loop);
	return res;
}

int audio_system_set_volume(float v)
{
	int res = VOL_OK;

	v = vol_clamp01(v);

	res = vol_ensure_connected();
	if (res != VOL_OK)
	{
		return res;
	}

	if (vol.loop == NULL)
	{
		return VOL_ERR_CONNECT;
	}

	pw_thread_loop_lock(vol.loop);
	pthread_mutex_lock(&vol.mu);

	vol_sink_t *s = vol_resolve_default_locked();
	if (s == NULL || s->proxy == NULL)
	{
		vol_set_error("no audio sink available");
		res = VOL_ERR_NO_SINK;
	}
	else
	{
		/*
		 * Build the Props pod. The adapter applies
		 * channelVolumes[i] * volume (master), so we write
		 * channelVolumes only — the same convention as WirePlumber's
		 * mixer API. Scaling the current values proportionally
		 * preserves any per-channel balance.
		 */
		uint8_t buf[1024];
		struct spa_pod_builder b = SPA_POD_BUILDER_INIT(buf, sizeof(buf));
		struct spa_pod_frame f;

		spa_pod_builder_push_object(
			&b, &f, SPA_TYPE_OBJECT_Props, SPA_PARAM_Props);

		if (s->n_chv > 0)
		{
			float cur = vol_clamp01(s->chv[0]);
			float newv[VOL_MAX_CHANNELS];

			for (uint32_t i = 0; i < s->n_chv; i++)
			{
				if (cur > 0.0001f && v > 0.0001f)
				{
					newv[i] = vol_clamp01(s->chv[i] * (v / cur));
				}
				else
				{
					/* From/to zero volume the ratio is
					 * undefined; reset balance. */
					newv[i] = v;
				}
			}

			spa_pod_builder_prop(&b, SPA_PROP_channelVolumes, 0);
			spa_pod_builder_array(
				&b,
				sizeof(float),
				SPA_TYPE_Float,
				s->n_chv,
				newv
			);
		}
		else
		{
			/* No channel volumes observed yet; the master volume
			 * scales all channels (n_volumes == 0 path in
			 * channelmix). */
			spa_pod_builder_prop(&b, SPA_PROP_volume, 0);
			spa_pod_builder_float(&b, v);
		}

		const struct spa_pod *pod = spa_pod_builder_pop(&b, &f);
		if (pod == NULL)
		{
			vol_set_error("failed to build volume props");
			res = VOL_ERR_PARAM;
		}
		else
		{
			int rc = pw_node_set_param(
				(struct pw_node *)s->proxy,
				SPA_PARAM_Props,
				0,
				pod
			);

			if (rc < 0)
			{
				vol_set_error("set volume failed: %s", spa_strerror(rc));
				res = VOL_ERR_PARAM;
			}
		}
	}

	pthread_mutex_unlock(&vol.mu);
	pw_thread_loop_unlock(vol.loop);
	return res;
}

int audio_system_set_mute(int muted)
{
	int res = VOL_OK;

	res = vol_ensure_connected();
	if (res != VOL_OK)
	{
		return res;
	}

	if (vol.loop == NULL)
	{
		return VOL_ERR_CONNECT;
	}

	pw_thread_loop_lock(vol.loop);
	pthread_mutex_lock(&vol.mu);

	vol_sink_t *s = vol_resolve_default_locked();
	if (s == NULL || s->proxy == NULL)
	{
		vol_set_error("no audio sink available");
		res = VOL_ERR_NO_SINK;
	}
	else
	{
		uint8_t buf[128];
		struct spa_pod_builder b = SPA_POD_BUILDER_INIT(buf, sizeof(buf));
		struct spa_pod_frame f;

		spa_pod_builder_push_object(
			&b, &f, SPA_TYPE_OBJECT_Props, SPA_PARAM_Props);
		spa_pod_builder_prop(&b, SPA_PROP_mute, 0);
		spa_pod_builder_bool(&b, muted ? true : false);

		const struct spa_pod *pod = spa_pod_builder_pop(&b, &f);
		if (pod == NULL)
		{
			vol_set_error("failed to build mute props");
			res = VOL_ERR_PARAM;
		}
		else
		{
			int rc = pw_node_set_param(
				(struct pw_node *)s->proxy,
				SPA_PARAM_Props,
				0,
				pod
			);

			if (rc < 0)
			{
				vol_set_error("set mute failed: %s", spa_strerror(rc));
				res = VOL_ERR_PARAM;
			}
		}
	}

	pthread_mutex_unlock(&vol.mu);
	pw_thread_loop_unlock(vol.loop);
	return res;
}

char *audio_system_last_error(void)
{
	/* Return a private copy: the Go wrapper frees it after converting. */
	pthread_mutex_lock(&vol.error_mu);
	char *copy = strdup(vol.last_error);
	pthread_mutex_unlock(&vol.error_mu);
	return copy;
}

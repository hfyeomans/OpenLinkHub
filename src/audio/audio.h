#pragma once
#include <stdint.h>

/*
 * Public API for the PipeWire audio engine.
 *
 * This header defines the control-plane interface used by the Go backend
 * via CGO. All functions declared here are safe to call from non–real-time
 * threads and do not perform audio processing directly.
 *
 * The implementation resides in audio.c.
 */

/* Returns the last error message set by the audio engine, or NULL if none. */
const char* audio_engine_last_error(void);

/*
 * Configures the audio engine.
 *
 * Must be called before audio_engine_start().
 * Returns 0 on success, negative value on error.
 */
int audio_engine_config(
    uint32_t rate,
    uint32_t channels,
    uint32_t polling_rate,
    uint32_t debug,
    uint32_t ring_frames,
    const char *lat,
    const char *max_lat,
    const char *preferred_sink_name,
    const char *preferred_sink_desc
);

/*
 * Starts the audio engine.
 *
 * Initializes PipeWire, creates the virtual sink, and enters the
 * processing loop. This call blocks until audio_engine_stop() is invoked.
 *
 * Returns 0 on normal shutdown, non-zero on error.
 */
int audio_engine_start(void);

/*
 * Requests the audio engine to stop.
 *
 * This function is asynchronous and signals the processing loop to exit.
 */
void audio_engine_stop(void);

/* Returns the status of audio engine. */
int audio_engine_running(void);

/* Sets gain (in dB) for a specific EQ band (0..9). */
void audio_engine_band(int i, float db);

/* Sets master gain (in dB). */
void audio_engine_master(float db);

/* Returns the PipeWire node name of the engine's virtual sink. */
const char* audio_engine_self_sink_name(void);

/* Returns the number of available PipeWire audio sinks. */
int audio_engine_sink_count(void);

/* Returns the serial number of the sink at index i. */
uint32_t audio_engine_sink_serial(int i);

/* Returns the internal name of the sink at index i. */
const char* audio_engine_sink_name(int i);

/* Returns the human-readable description of the sink at index i. */
const char* audio_engine_sink_desc(int i);

/*
 * Selects the target output sink by serial number.
 *
 * Returns 0 on success, -1 if the sink does not exist.
 */
int audio_engine_set_target_sink(uint32_t serial);

/* Returns the serial number of the currently selected sink, or 0 if none. */
uint32_t audio_engine_current_sink_serial(void);

/* Returns the name of the currently selected sink, or NULL if none. */
const char* audio_engine_current_sink_name(void);

/* Returns the description of the currently selected sink, or NULL if none. */
const char* audio_engine_current_sink_desc(void);

/*
 * System volume control (implemented in volume.c).
 *
 * These functions run on an INDEPENDENT PipeWire connection (own
 * pw_thread_loop + pw_context + pw_core + registry) and never touch the
 * virtual-sink DSP engine in audio.c.
 *
 * Volume is LINEAR 0..1 (1.0 = no attenuation, 0.0 = silence). The UI
 * maps 0-100 linearly onto this range. This diverges from wpctl's cubic
 * display scale, but matches the underlying linear node volumes that
 * `wpctl get-volume` also reports.
 *
 * The target sink is resolved per call:
 *   1. the metadata "default" object's default.audio.sink entry,
 *   2. otherwise the first Audio/Sink node that is not the engine's own
 *      virtual sink,
 *   3. otherwise the first Audio/Sink node.
 *
 * All functions are safe to call from any thread, are bounded in time
 * (no unbounded blocking; the only longer wait is the bounded initial
 * connection sync), and return 0 on success or a negative error code:
 *   -1 no sink available / sink Props not available
 *   -2 PipeWire connection failed or unavailable (retry-throttled)
 *   -3 parameter (pod) build or write failed
 */

/* Returns a malloc'd copy of the last error message set by the system
 * volume module (caller frees), or an empty string. */
char* audio_system_last_error(void);

/*
 * Reads the resolved default sink's volume (linear 0..1) and mute state.
 * Either output pointer may be NULL to skip that value.
 * Returns 0 on success, negative error code on failure.
 */
int audio_system_get_volume(float *volume, int *muted);

/*
 * Sets the resolved default sink's volume. v is clamped to linear 0..1.
 * Existing per-channel volumes are scaled proportionally to preserve
 * balance. Returns 0 on success, negative error code on failure.
 */
int audio_system_set_volume(float v);

/*
 * Mutes (muted != 0) or unmutes (muted == 0) the resolved default sink.
 * Returns 0 on success, negative error code on failure.
 */
int audio_system_set_mute(int muted);
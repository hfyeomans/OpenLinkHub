"use strict";

(function () {
    const display = document.getElementById("swDisplay");
    const toggle = document.getElementById("swToggle");
    const lap = document.getElementById("swLap");
    const reset = document.getElementById("swReset");
    const laps = document.getElementById("swLaps");

    let running = false;
    let startAt = 0; // performance.now() when started
    let elapsed = 0; // accumulated ms while stopped
    let tick = null;

    const current = function () {
        return elapsed + (running ? performance.now() - startAt : 0);
    };

    const fmt = function (ms) {
        const cs = Math.floor(ms / 10) % 100;
        const s = Math.floor(ms / 1000) % 60;
        const m = Math.floor(ms / 60000);
        return String(m).padStart(2, "0") + ":" + String(s).padStart(2, "0") + "." + String(cs).padStart(2, "0");
    };

    const render = function () {
        display.textContent = fmt(current());
    };

    const start = function () {
        running = true;
        startAt = performance.now();
        tick = setInterval(render, 33);
        toggle.textContent = "Stop";
        lap.disabled = false;
        reset.disabled = false;
    };

    const stop = function () {
        if (!running) return;
        running = false;
        elapsed += performance.now() - startAt;
        clearInterval(tick);
        toggle.textContent = "Start";
        render();
    };

    toggle.addEventListener("click", function () {
        if (running) {
            stop();
        } else {
            start();
        }
    });

    lap.addEventListener("click", function () {
        const li = document.createElement("li");
        li.textContent = fmt(current());
        laps.insertBefore(li, laps.firstChild);
        while (laps.children.length > 5) {
            laps.removeChild(laps.lastChild);
        }
    });

    reset.addEventListener("click", function () {
        stop();
        elapsed = 0;
        laps.textContent = "";
        lap.disabled = true;
        reset.disabled = true;
        render();
    });

    render();
})();

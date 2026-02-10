// app.js — StarGo Satellite Tracker
//
// Connects to the SSE keyframe stream and renders satellite positions
// on a Cesium 3D globe using PointPrimitiveCollection (Phase 5b).
// Linearly interpolates ECEF positions between keyframes for smooth motion.

'use strict';

// ─── Configuration ──────────────────────────────────────────────────────────
// Set your Cesium Ion access token below. Get a free one at:
// https://ion.cesium.com/signup/
// Without a token the globe may lack imagery but satellites still render.
var CESIUM_ION_TOKEN = '';

var CONFIG = {
    sseEndpoint:        '/api/v1/stream/keyframes?step=5&horizon=600&trail=20',
    authToken:          '',        // Bearer token (set if STARGO_AUTH_ENABLED=true)
    reconnectBaseMs:    1000,      // Initial reconnect delay
    reconnectMaxMs:     30000,     // Max reconnect delay
    earthRadiusMeters:  6371000,
};

// ─── Application State ──────────────────────────────────────────────────────

var state = {
    viewer:          null,
    pointCollection: null,
    trailCollection: null,
    satellites:      new Map(),  // NORAD ID -> { point, polyline }

    prevKeyframe:    null,       // { time: epoch ms, sats: Map<id, [x,y,z]> }
    currKeyframe:    null,

    connected:       false,
    paused:          false,
    speed:           1,
    trailsEnabled:   true,
    tleAge:          0,          // Seconds
    datasetEpoch:    null,       // ISO string
    satCount:        0,

    abortController: null,
};

// Scratch Cartesian3 reused every frame to avoid GC pressure.
var _scratch = null;

// ─── Bootstrap ──────────────────────────────────────────────────────────────

document.addEventListener('DOMContentLoaded', init);

function init() {
    setupCesium();
    setupControls();
    connectSSE();
}

// ─── Cesium Setup ───────────────────────────────────────────────────────────

function setupCesium() {
    window.CESIUM_BASE_URL =
        'https://cesium.com/downloads/cesiumjs/releases/1.119/Build/Cesium/';

    if (CESIUM_ION_TOKEN) {
        Cesium.Ion.defaultAccessToken = CESIUM_ION_TOKEN;
    }

    var viewerOpts = {
        animation:              false,
        timeline:               false,
        baseLayerPicker:        false,
        geocoder:               false,
        fullscreenButton:       false,
        sceneModePicker:        false,
        selectionIndicator:     false,
        infoBox:                false,
        navigationHelpButton:   false,
        shouldAnimate:          true,
        requestRenderMode:      false,
    };

    // Without an Ion token, disable default Ion imagery to avoid console errors.
    if (!CESIUM_ION_TOKEN) {
        viewerOpts.imageryProvider = false;
    }

    state.viewer = new Cesium.Viewer('cesiumContainer', viewerOpts);

    // If no Ion token, add bundled NaturalEarthII textures as base layer.
    if (!CESIUM_ION_TOKEN) {
        try {
            var provider = new Cesium.TileMapServiceImageryProvider({
                url: Cesium.buildModuleUrl('Assets/Textures/NaturalEarthII'),
            });
            state.viewer.imageryLayers.addImageryProvider(provider);
        } catch (e) {
            console.warn('Could not load fallback imagery:', e.message);
        }
    }

    // View Earth from space.
    state.viewer.camera.setView({
        destination: Cesium.Cartesian3.fromDegrees(0, 20, 30000000),
    });

    // PointPrimitiveCollection: efficient batch rendering for 2000+ satellites.
    state.pointCollection = state.viewer.scene.primitives.add(
        new Cesium.PointPrimitiveCollection()
    );

    // PolylineCollection: orbital trails behind each satellite.
    state.trailCollection = state.viewer.scene.primitives.add(
        new Cesium.PolylineCollection()
    );

    _scratch = new Cesium.Cartesian3();

    // Interpolation runs every frame.
    state.viewer.scene.preRender.addEventListener(interpolate);

    console.log('Cesium viewer initialized');
}

// ─── SSE Connection ─────────────────────────────────────────────────────────
// Uses fetch + ReadableStream instead of EventSource so we can send
// Authorization headers when auth is enabled.

function connectSSE() {
    var retryDelay = CONFIG.reconnectBaseMs;

    function loop() {
        setConnectionStatus('connecting');
        streamKeyframes()
            .then(function () {
                // Stream ended cleanly — reconnect.
                retryDelay = CONFIG.reconnectBaseMs;
                setConnectionStatus('reconnecting');
                setTimeout(loop, retryDelay);
            })
            .catch(function (err) {
                console.error('SSE error:', err.message || err);
                setConnectionStatus('reconnecting');
                setTimeout(function () {
                    retryDelay = Math.min(retryDelay * 2, CONFIG.reconnectMaxMs);
                    loop();
                }, retryDelay);
            });
    }

    loop();
}

function streamKeyframes() {
    state.abortController = new AbortController();

    var headers = { 'Accept': 'text/event-stream' };
    if (CONFIG.authToken) {
        headers['Authorization'] = 'Bearer ' + CONFIG.authToken;
    }

    return fetch(CONFIG.sseEndpoint, {
        headers: headers,
        signal:  state.abortController.signal,
    }).then(function (response) {
        if (!response.ok) {
            throw new Error('HTTP ' + response.status);
        }
        setConnectionStatus('connected');

        var reader  = response.body.getReader();
        var decoder = new TextDecoder();
        var buffer  = '';

        function read() {
            return reader.read().then(function (result) {
                if (result.done) return;

                buffer += decoder.decode(result.value, { stream: true });

                // SSE messages are delimited by double newline.
                var parts = buffer.split('\n\n');
                buffer = parts.pop(); // Keep trailing incomplete fragment.

                for (var i = 0; i < parts.length; i++) {
                    parseSSEBlock(parts[i]);
                }

                return read();
            });
        }

        return read();
    });
}

function parseSSEBlock(block) {
    var lines = block.split('\n');
    for (var i = 0; i < lines.length; i++) {
        var line = lines[i];
        if (line.indexOf('data: ') === 0) {
            try {
                var data = JSON.parse(line.slice(6));
                handleMessage(data);
            } catch (e) {
                console.warn('Invalid SSE JSON:', e.message);
            }
        }
        // Lines starting with ':' are SSE comments (keep-alive) — ignored.
    }
}

// ─── Message Handling ───────────────────────────────────────────────────────

function handleMessage(msg) {
    if (msg.type === 'metadata') {
        handleMetadata(msg);
    } else if (msg.type === 'keyframe_batch') {
        handleKeyframeBatch(msg);
    } else {
        console.warn('Unknown message type:', msg.type);
    }
}

function handleMetadata(msg) {
    state.datasetEpoch = msg.dataset_epoch;
    state.tleAge = msg.tle_age_seconds;
    updateTLEDisplay();
    console.log('Metadata:', msg.dataset_epoch, 'age', msg.tle_age_seconds + 's');
}

function handleKeyframeBatch(msg) {
    // Shift: current becomes previous.
    state.prevKeyframe = state.currKeyframe;

    var satMap = new Map();
    var sats = msg.sat;
    for (var i = 0; i < sats.length; i++) {
        satMap.set(sats[i].id, sats[i].p);
    }

    state.currKeyframe = {
        time:       new Date(msg.t).getTime(),
        satellites: satMap,
    };

    state.satCount = sats.length;

    // Create/update points and trails for each satellite.
    for (var j = 0; j < sats.length; j++) {
        var sat = sats[j];
        var pos = new Cesium.Cartesian3(sat.p[0], sat.p[1], sat.p[2]);
        var satState = state.satellites.get(sat.id);

        if (!satState) {
            // New satellite — create point and trail polyline.
            var point = state.pointCollection.add({
                position:        pos,
                pixelSize:       3,
                color:           altitudeColor(sat.p),
                scaleByDistance:  new Cesium.NearFarScalar(1e7, 1.5, 5e7, 0.5),
            });
            var polyline = state.trailCollection.add({
                positions: [pos],
                width:     1,
                material:  Cesium.Material.fromType('Color', {
                    color: altitudeColor(sat.p).withAlpha(0.3),
                }),
                show: state.trailsEnabled,
            });
            state.satellites.set(sat.id, { point: point, polyline: polyline });
        }

        // Update trail from server-provided positions.
        satState = state.satellites.get(sat.id);
        if (satState && sat.tr && sat.tr.length > 0 && state.trailsEnabled) {
            var positions = new Array(sat.tr.length);
            for (var k = 0; k < sat.tr.length; k++) {
                positions[k] = new Cesium.Cartesian3(sat.tr[k][0], sat.tr[k][1], sat.tr[k][2]);
            }
            satState.polyline.positions = positions;
        }
    }

    updateStatusDisplay();
}

// ─── Interpolation ──────────────────────────────────────────────────────────
// Called every frame (preRender). Linearly interpolates ECEF positions between
// the previous and current keyframe for smooth satellite motion.
//
// Formula: P(t) = P_prev + (P_curr - P_prev) * alpha
// where alpha = clamp01((now - prevTime) * speed / (currTime - prevTime))

function interpolate() {
    if (state.paused) return;

    var prev = state.prevKeyframe;
    var curr = state.currKeyframe;
    if (!prev || !curr) return;

    var duration = curr.time - prev.time;
    if (duration <= 0) return;

    var elapsed = (Date.now() - prev.time) * state.speed;
    var alpha   = elapsed / duration;
    if (alpha < 0) alpha = 0;
    if (alpha > 1) alpha = 1;

    var satellites = state.satellites;
    var prevSats   = prev.satellites;
    var currSats   = curr.satellites;

    satellites.forEach(function (satState, id) {
        var p0 = prevSats.get(id);
        var p1 = currSats.get(id);
        if (!p0 || !p1) return;

        _scratch.x = p0[0] + (p1[0] - p0[0]) * alpha;
        _scratch.y = p0[1] + (p1[1] - p0[1]) * alpha;
        _scratch.z = p0[2] + (p1[2] - p0[2]) * alpha;
        satState.point.position = _scratch; // PointPrimitive clones internally.
    });

    // Continuously update TLE age from dataset epoch.
    if (state.datasetEpoch) {
        state.tleAge = (Date.now() - new Date(state.datasetEpoch).getTime()) / 1000;
        updateTLEDisplay();
    }
}

// ─── Altitude-Based Coloring ────────────────────────────────────────────────
//   LEO low  (< 600 km):  green
//   LEO high (< 2000 km): cyan
//   MEO      (< 20000 km): yellow
//   GEO      (< 40000 km): orange
//   HEO      (> 40000 km): red

function altitudeColor(ecef) {
    var r     = Math.sqrt(ecef[0] * ecef[0] + ecef[1] * ecef[1] + ecef[2] * ecef[2]);
    var altKm = (r - CONFIG.earthRadiusMeters) / 1000;

    if (altKm < 600)   return Cesium.Color.LIME;
    if (altKm < 2000)  return Cesium.Color.CYAN;
    if (altKm < 20000) return Cesium.Color.YELLOW;
    if (altKm < 40000) return Cesium.Color.ORANGE;
    return Cesium.Color.RED;
}

// ─── UI Updates ─────────────────────────────────────────────────────────────

function setConnectionStatus(status) {
    var el = document.getElementById('connectionStatus');
    if (!el) return;

    if (status === 'connected') {
        state.connected = true;
        el.textContent = 'Connected';
        el.className = 'status-connected';
    } else if (status === 'reconnecting') {
        state.connected = false;
        el.textContent = 'Reconnecting...';
        el.className = 'status-reconnecting';
    } else {
        state.connected = false;
        el.textContent = 'Connecting...';
        el.className = 'status-disconnected';
    }
}

function updateTLEDisplay() {
    var ageEl   = document.getElementById('tleAge');
    var epochEl = document.getElementById('datasetEpoch');

    if (ageEl) {
        var totalMin = Math.floor(state.tleAge / 60);
        var hours    = Math.floor(totalMin / 60);
        var minutes  = totalMin % 60;
        var text;

        if (hours > 0) {
            text = 'TLE Age: ' + hours + 'h ' + minutes + 'm';
        } else {
            text = 'TLE Age: ' + minutes + 'm';
        }

        ageEl.textContent = text;
        ageEl.className = state.tleAge > 3600 ? 'tle-stale' : 'tle-fresh';
    }

    if (epochEl && state.datasetEpoch) {
        epochEl.textContent = 'Dataset: ' + state.datasetEpoch;
    }
}

function updateStatusDisplay() {
    var el = document.getElementById('satCount');
    if (el) {
        el.textContent = state.satCount + ' satellites';
    }
}

// ─── Controls ───────────────────────────────────────────────────────────────

function setupControls() {
    var pauseBtn = document.getElementById('pauseBtn');
    if (pauseBtn) {
        pauseBtn.addEventListener('click', function () {
            state.paused = !state.paused;
            pauseBtn.textContent = state.paused ? 'Play' : 'Pause';
        });
    }

    var trailBtn = document.getElementById('trailBtn');
    if (trailBtn) {
        trailBtn.addEventListener('click', function () {
            state.trailsEnabled = !state.trailsEnabled;
            trailBtn.textContent = state.trailsEnabled ? 'Trails: ON' : 'Trails: OFF';
            trailBtn.classList.toggle('active', state.trailsEnabled);
            state.trailCollection.show = state.trailsEnabled;
        });
    }

    var speedBtns = document.querySelectorAll('.speed-btn');
    for (var i = 0; i < speedBtns.length; i++) {
        speedBtns[i].addEventListener('click', function () {
            state.speed = parseFloat(this.dataset.speed);
            for (var j = 0; j < speedBtns.length; j++) {
                speedBtns[j].classList.remove('active');
            }
            this.classList.add('active');
        });
    }
}

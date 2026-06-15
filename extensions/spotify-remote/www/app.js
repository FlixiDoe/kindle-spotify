var refreshTimer = null;
var refreshSeconds = 8;
var lastState = null;

function el(id) {
  return document.getElementById(id);
}

function show(id, visible) {
  el(id).className = visible ? el(id).className.replace(" hidden", "") : addHidden(el(id).className);
}

function addHidden(cls) {
  return cls.indexOf("hidden") >= 0 ? cls : cls + " hidden";
}

function setError(message) {
  if (!message) {
    show("error", false);
    el("error").textContent = "";
    return;
  }
  el("error").textContent = message;
  show("error", true);
}

function api(path, opts) {
  opts = opts || {};
  return fetch(path, opts).then(function(res) {
    return res.json().catch(function() {
      return {};
    }).then(function(body) {
      if (!res.ok || body.error) {
        throw new Error(body.error || ("HTTP " + res.status));
      }
      return body;
    });
  });
}

function fmt(ms) {
  ms = ms || 0;
  var sec = Math.floor(ms / 1000);
  var min = Math.floor(sec / 60);
  sec = sec % 60;
  return min + ":" + (sec < 10 ? "0" + sec : sec);
}

function loadConfig() {
  api("/api/config").then(function(cfg) {
    refreshSeconds = cfg.refresh_seconds || 8;
    el("clientId").value = cfg.client_id || "";
    if (!cfg.client_id || cfg.client_id.indexOf("PASTE_") === 0) {
      show("setup", true);
    }
  }).catch(function(err) {
    setError(err.message);
  });
}

function saveConfig() {
  api("/api/config", {
    method: "POST",
    headers: {"Content-Type": "application/json"},
    body: JSON.stringify({client_id: el("clientId").value})
  }).then(function() {
    el("setupMsg").textContent = "Saved.";
    show("setup", false);
  }).catch(function(err) {
    setError(err.message);
  });
}

function login() {
  setError("");
  api("/api/login").then(function(data) {
    show("manual", true);
    window.location.href = data.auth_url;
  }).catch(function(err) {
    setError(err.message);
  });
}

function manualLogin() {
  api("/api/manual-callback", {
    method: "POST",
    headers: {"Content-Type": "application/json"},
    body: JSON.stringify({value: el("manualCode").value})
  }).then(function() {
    show("manual", false);
    refresh();
  }).catch(function(err) {
    setError(err.message);
  });
}

function refresh() {
  api("/api/status").then(function(state) {
    lastState = state;
    setError("");
    renderState(state);
  }).catch(function(err) {
    setError(err.message);
  });
}

function renderState(state) {
  if (!state || !state.item) {
    el("title").textContent = "No active Spotify device";
    el("artist").textContent = "Start playback in Spotify on another device.";
    el("album").textContent = "";
    el("time").textContent = "0:00 / 0:00";
    el("bar").style.width = "0%";
    el("cover").removeAttribute("src");
    return;
  }

  el("title").textContent = state.item.name || "Unknown title";
  el("artist").textContent = (state.item.artists || []).map(function(a) { return a.name; }).join(", ");
  el("album").textContent = state.item.album ? state.item.album.name : "";
  el("time").textContent = fmt(state.progress_ms) + " / " + fmt(state.item.duration_ms);
  el("bar").style.width = Math.max(0, Math.min(100, (state.progress_ms || 0) * 100 / (state.item.duration_ms || 1))) + "%";
  el("playBtn").textContent = state.is_playing ? "Pause" : "Play";
  el("shuffleBtn").textContent = state.shuffle_state ? "Shuffle On" : "Shuffle Off";
  el("repeatBtn").textContent = "Repeat " + (state.repeat_state || "off");

  var images = state.item.album && state.item.album.images ? state.item.album.images : [];
  if (images.length) {
    el("cover").src = "/api/cover?url=" + encodeURIComponent(images[images.length - 1].url);
  } else {
    el("cover").removeAttribute("src");
  }
}

function control(action, extra) {
  var body = extra || {};
  body.action = action;
  api("/api/control", {
    method: "POST",
    headers: {"Content-Type": "application/json"},
    body: JSON.stringify(body)
  }).then(function() {
    setTimeout(refresh, 900);
  }).catch(function(err) {
    setError(err.message);
  });
}

function togglePlay() {
  control(lastState && lastState.is_playing ? "pause" : "play");
}

function volume(delta) {
  var current = lastState && lastState.device ? lastState.device.volume_percent : 50;
  control("volume", {volume_percent: Math.max(0, Math.min(100, current + delta))});
}

function toggleShuffle() {
  control("shuffle", {state: !(lastState && lastState.shuffle_state)});
}

function toggleRepeat() {
  var current = lastState ? lastState.repeat_state : "off";
  var next = current === "off" ? "context" : current === "context" ? "track" : "off";
  control("repeat", {state: next});
}

function devices() {
  api("/api/devices").then(function(data) {
    var list = el("deviceList");
    list.innerHTML = "";
    (data.devices || []).forEach(function(device) {
      var b = document.createElement("button");
      b.textContent = (device.is_active ? "* " : "") + device.name + " (" + device.type + ")";
      b.onclick = function() {
        control("transfer", {device_id: device.id});
        show("devices", false);
      };
      list.appendChild(b);
    });
    if (!list.children.length) {
      list.textContent = "No devices found.";
    }
    show("devices", true);
  }).catch(function(err) {
    setError(err.message);
  });
}

function boot() {
  loadConfig();
  el("saveConfigBtn").onclick = saveConfig;
  el("loginBtn").onclick = login;
  el("manualBtn").onclick = manualLogin;
  el("prevBtn").onclick = function() { control("previous"); };
  el("playBtn").onclick = togglePlay;
  el("nextBtn").onclick = function() { control("next"); };
  el("volDownBtn").onclick = function() { volume(-10); };
  el("volUpBtn").onclick = function() { volume(10); };
  el("shuffleBtn").onclick = toggleShuffle;
  el("repeatBtn").onclick = toggleRepeat;
  el("devicesBtn").onclick = devices;
  el("refreshBtn").onclick = refresh;
  refresh();
  refreshTimer = setInterval(refresh, refreshSeconds * 1000);
}

boot();

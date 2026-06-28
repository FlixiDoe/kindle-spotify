var refreshTimer = null;
var refreshSeconds = 8;
var lastState = null;
var rateLimitTimer = null;
var rateLimitActive = false;
var controlButtonIDs = ["prevBtn", "playBtn", "nextBtn", "volDownBtn", "volUpBtn", "shuffleBtn", "repeatBtn"];

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
        var err = new Error(body.error || ("HTTP " + res.status));
        err.loginRequired = !!body.login_required;
        if (res.status === 429 || body.error === "rate_limited") {
          err.rateLimited = true;
          err.retryAfter = Number(body.retry_after || res.headers.get("Retry-After") || 5);
          err.message = body.message || ("Spotify rate limit reached - retrying in " + err.retryAfter + "s");
        }
        throw err;
      }
      return body;
    });
  });
}

function handleAPIError(err) {
  if (err.rateLimited) {
    startRateLimitCountdown(err.retryAfter || 5);
    return;
  }
  setError(err.message);
  if (err.loginRequired) {
    show("manual", true);
  }
}

function setControlsDisabled(disabled) {
  controlButtonIDs.forEach(function(id) {
    el(id).disabled = disabled;
  });
}

function startRefreshTimer() {
  if (refreshTimer) {
    clearInterval(refreshTimer);
  }
  refreshTimer = setInterval(refresh, refreshSeconds * 1000);
}

function startRateLimitCountdown(seconds) {
  var remaining = Math.max(1, Number(seconds) || 5);
  rateLimitActive = true;
  setControlsDisabled(true);
  if (refreshTimer) {
    // Browser polling is paused during Retry-After so the client does not independently retry the local server.
    clearInterval(refreshTimer);
    refreshTimer = null;
  }
  if (rateLimitTimer) {
    clearInterval(rateLimitTimer);
  }
  function render() {
    el("rateLimitBanner").textContent = "Spotify rate limit reached - retrying in " + remaining + " s";
    show("rateLimitBanner", true);
  }
  render();
  rateLimitTimer = setInterval(function() {
    remaining -= 1;
    if (remaining <= 0) {
      clearInterval(rateLimitTimer);
      rateLimitTimer = null;
      rateLimitActive = false;
      setControlsDisabled(false);
      show("rateLimitBanner", false);
      startRefreshTimer();
      refresh();
      return;
    }
    render();
  }, 1000);
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
    handleAPIError(err);
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
    handleAPIError(err);
  });
}

function login() {
  setError("");
  api("/api/login").then(function(data) {
    show("manual", true);
    window.location.href = data.auth_url;
  }).catch(function(err) {
    handleAPIError(err);
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
    handleAPIError(err);
  });
}

function refresh() {
  if (rateLimitActive) {
    return;
  }
  api("/api/status").then(function(state) {
    lastState = state;
    setError("");
    renderState(state);
  }).catch(function(err) {
    handleAPIError(err);
  });
}

function renderState(state) {
  if (!state || !state.item) {
    el("title").textContent = "No active Spotify device";
    el("artist").textContent = "Start playback in Spotify on another device.";
    el("album").textContent = "";
    el("time").textContent = "0:00 / 0:00";
    el("device").textContent = "";
    el("bar").style.width = "0%";
    el("cover").removeAttribute("src");
    return;
  }

  el("title").textContent = state.item.name || "Unknown title";
  el("artist").textContent = (state.item.artists || []).map(function(a) { return a.name; }).join(", ");
  el("album").textContent = state.item.album ? state.item.album.name : "";
  el("time").textContent = fmt(state.progress_ms) + " / " + fmt(state.item.duration_ms);
  el("device").textContent = state.device && state.device.name ? "Device: " + state.device.name : "";
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
  if (rateLimitActive) {
    return;
  }
  var body = extra || {};
  body.action = action;
  api("/api/control", {
    method: "POST",
    headers: {"Content-Type": "application/json"},
    body: JSON.stringify(body)
  }).then(function() {
    setTimeout(refresh, 900);
  }).catch(function(err) {
    handleAPIError(err);
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
  if (rateLimitActive) {
    return;
  }
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
  startRefreshTimer();
}

boot();

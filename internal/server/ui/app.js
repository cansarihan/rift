"use strict";

const COLORS = { likely: "#ff3b5c", possible: "#ffb347", unlikely: "#3fb8af", clean: "#3fb8af" };
const $ = (id) => document.getElementById(id);

const canvas = $("fault");
const ctx = canvas.getContext("2d");
let dpr = Math.max(1, window.devicePixelRatio || 1);
let W = 0, H = 0;
let fault = [];
let nodes = [];
let sweep = { on: false, y: 0 };
let token = new URLSearchParams(location.search).get("token") || "";

function auth(path) {
  if (!token) return path;
  return path + (path.includes("?") ? "&" : "?") + "token=" + encodeURIComponent(token);
}

function resize() {
  dpr = Math.max(1, window.devicePixelRatio || 1);
  const rect = canvas.getBoundingClientRect();
  W = rect.width; H = rect.height;
  canvas.width = Math.floor(W * dpr);
  canvas.height = Math.floor(H * dpr);
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  buildFault();
}

function buildFault() {
  fault = [];
  const cx = W * 0.5;
  const steps = Math.max(10, Math.floor(H / 26));
  let seed = 1337;
  const rnd = () => { seed = (seed * 1103515245 + 12345) & 0x7fffffff; return seed / 0x7fffffff; };
  for (let i = 0; i <= steps; i++) {
    const y = (H * i) / steps;
    const jitter = (rnd() - 0.5) * 46;
    fault.push({ x: cx + jitter, y });
  }
}

function faultX(y) {
  for (let i = 1; i < fault.length; i++) {
    if (y <= fault[i].y) {
      const a = fault[i - 1], b = fault[i];
      const t = (y - a.y) / Math.max(1, b.y - a.y);
      return a.x + (b.x - a.x) * t;
    }
  }
  return W * 0.5;
}

function addNode(d) {
  const flagged = d.band === "likely" || d.band === "possible";
  const y = 40 + Math.random() * (H - 80);
  nodes.push({
    name: d.name, band: d.band, status: d.status, score: d.score,
    y, side: nodes.length % 2 === 0 ? -1 : 1,
    r: flagged ? 5.5 : 3, born: performance.now(),
  });
  if (nodes.length > 400) nodes.shift();
}

function draw(now) {
  ctx.clearRect(0, 0, W, H);

  // the fault line itself
  ctx.beginPath();
  ctx.moveTo(fault[0].x, fault[0].y);
  for (const p of fault) ctx.lineTo(p.x, p.y);
  ctx.strokeStyle = "rgba(124,134,153,0.28)";
  ctx.lineWidth = 1.4;
  ctx.stroke();

  // sweep while a comparison runs
  if (sweep.on) {
    sweep.y = (sweep.y + 2.4) % H;
    const grad = ctx.createLinearGradient(0, sweep.y - 60, 0, sweep.y + 10);
    grad.addColorStop(0, "rgba(255,59,92,0)");
    grad.addColorStop(1, "rgba(255,59,92,0.22)");
    ctx.fillStyle = grad;
    ctx.fillRect(0, sweep.y - 60, W, 70);
    ctx.strokeStyle = "rgba(255,59,92,0.5)";
    ctx.lineWidth = 1;
    ctx.beginPath(); ctx.moveTo(0, sweep.y); ctx.lineTo(W, sweep.y); ctx.stroke();
  }

  // nodes as rifts branching off the fault
  for (const n of nodes) {
    const color = COLORS[n.band] || "#7c8699";
    const fx = faultX(n.y);
    const nx = fx + n.side * (40 + n.score * 1.4);
    const age = (now - n.born) / 1000;
    const pulse = Math.max(0, 1 - age / 1.4);

    ctx.beginPath();
    ctx.moveTo(fx, n.y);
    const midx = (fx + nx) / 2 + n.side * 10;
    ctx.lineTo(midx, n.y - 6);
    ctx.lineTo(nx, n.y);
    ctx.strokeStyle = color;
    ctx.globalAlpha = n.band === "unlikely" ? 0.28 : 0.6;
    ctx.lineWidth = 1.1;
    ctx.stroke();
    ctx.globalAlpha = 1;

    ctx.beginPath();
    ctx.arc(nx, n.y, n.r + pulse * 6, 0, Math.PI * 2);
    ctx.fillStyle = color;
    ctx.globalAlpha = 0.12 + pulse * 0.25;
    ctx.fill();
    ctx.globalAlpha = 1;

    ctx.beginPath();
    ctx.arc(nx, n.y, n.r, 0, Math.PI * 2);
    ctx.fillStyle = color;
    ctx.globalAlpha = n.band === "unlikely" ? 0.5 : 1;
    ctx.shadowColor = color;
    ctx.shadowBlur = n.band === "unlikely" ? 0 : 10;
    ctx.fill();
    ctx.shadowBlur = 0;
    ctx.globalAlpha = 1;
  }

  requestAnimationFrame(draw);
}

function setBand(band) {
  const el = $("band");
  el.className = "band " + band;
  $("band-label").textContent = band;
}

function renderList(deltas) {
  const ol = $("deltas");
  ol.innerHTML = "";
  const flagged = deltas.filter((d) => d.band === "likely" || d.band === "possible");
  const show = (flagged.length ? flagged : deltas).slice(0, 80);
  $("empty").style.display = show.length ? "none" : "block";
  $("list-count").textContent = flagged.length;
  for (const d of show) {
    const li = document.createElement("li");
    li.className = d.band;
    li.innerHTML =
      '<span class="score">' + d.score + '</span>' +
      '<span class="fn"><div class="name">' + escapeHTML(d.name) + '</div>' +
      '<div class="why">' + escapeHTML((d.reasons || []).join(" · ")) + '</div></span>' +
      '<span class="status">' + d.status + '</span>';
    ol.appendChild(li);
  }
}

function escapeHTML(s) {
  return (s || "").replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
}

let streamDeltas = [];

async function start() {
  $("rerun").disabled = true;
  nodes = []; streamDeltas = [];
  $("deltas").innerHTML = "";
  sweep.on = true;
  setBand("possible");
  $("band-label").textContent = "scanning";

  const res = await fetch(auth("/api/v1/scans"), { method: "POST" });
  const scan = await res.json();
  const id = scan.id;

  const es = new EventSource(auth("/api/v1/scans/" + id + "/stream"));
  es.onmessage = (ev) => {
    let msg;
    try { msg = JSON.parse(ev.data); } catch { return; }
    if (msg.type === "delta" && msg.delta) {
      streamDeltas.push(msg.delta);
      addNode(msg.delta);
    } else if (msg.type === "progress") {
      if (msg.total) $("progress-fill").style.width = Math.round((100 * msg.done) / msg.total) + "%";
    } else if (msg.type === "done") {
      es.close();
      finish(id);
    }
  };
  es.onerror = () => { es.close(); finish(id); };
}

async function finish(id) {
  try {
    const res = await fetch(auth("/api/v1/scans/" + id));
    const scan = await res.json();
    sweep.on = false;
    $("progress-fill").style.width = "100%";
    setBand(scan.band || "clean");
    $("s-compared").textContent = scan.compared || 0;
    $("s-modified").textContent = scan.modified || 0;
    $("s-added").textContent = scan.added || 0;
    $("s-removed").textContent = scan.removed || 0;
    $("s-flagged").textContent = scan.flagged || 0;
    renderList(scan.deltas || streamDeltas);
    if (scan.error) $("meta").textContent = "error: " + scan.error;
  } finally {
    $("rerun").disabled = false;
  }
}

async function init() {
  resize();
  requestAnimationFrame(draw);
  window.addEventListener("resize", resize);
  $("rerun").addEventListener("click", start);
  try {
    const res = await fetch(auth("/api/v1/status"));
    const st = await res.json();
    $("old-path").textContent = st.old || "—";
    $("new-path").textContent = st.new || "—";
    $("meta").textContent = (st.platform || "") + " · rift " + (st.version || "");
  } catch (e) { /* status is best-effort */ }
  start();
}

init();

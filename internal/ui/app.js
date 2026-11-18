/* fsx 桌面客户端 — 石墨仪表盘
   数据来自 /api/events（1Hz SSE），列表按键增量更新，避免整表重建打断动画与滚动。 */

const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => Array.from(el.querySelectorAll(s));
const SVGNS = "http://www.w3.org/2000/svg";

let S = { self: {}, peers: [], jobs: [], messages: [], relay: "off" };
const UI = {
  tab: "transfers", filter: "all", query: "", peerQuery: "", sort: "recent",
  target: null, pickTarget: null, drawer: null, conv: null,
  hidden: new Set(), upload: null, peak: 0, connected: false,
  dragDepth: 0, pendingOut: [],
};
const LIVE = ["pending", "connecting", "waiting", "active"];
const RUNNING = ["connecting", "waiting", "active"];

/* ── 基础工具 ─────────────────────────────── */
function el(tag, cls, text) {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
}
function icon(name, cls) {
  const s = document.createElementNS(SVGNS, "svg");
  s.setAttribute("class", "ic" + (cls ? " " + cls : ""));
  const u = document.createElementNS(SVGNS, "use");
  u.setAttribute("href", "#i-" + name);
  s.appendChild(u);
  return s;
}
function iconBtn(name, title, fn, cls) {
  const b = el("button", "iconbtn" + (cls ? " " + cls : ""));
  b.type = "button";
  b.title = title;
  b.setAttribute("aria-label", title);
  b.append(icon(name));
  b.onclick = (e) => { e.stopPropagation(); fn(e); };
  return b;
}
function btn(label, cls, iconName, fn) {
  const b = el("button", "btn " + cls);
  b.type = "button";
  if (iconName) b.append(icon(iconName));
  b.append(document.createTextNode(label));
  b.onclick = (e) => { e.stopPropagation(); fn(e); };
  return b;
}
function shortID(id) { return id ? id.slice(0, 8) : ""; }
function peerKey(p) { return p.id || p.addr || p.name || ""; }
function fmtSize(n) {
  if (!n || n < 0) return "0 B";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0, v = n;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return (v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)) + " " + u[i];
}
function fmtSpeed(n) { return n > 0 ? fmtSize(n) + "/s" : "0 B/s"; }
function fmtClock(t) {
  if (!t) return "—";
  const d = new Date(t * 1000);
  const p = (x) => String(x).padStart(2, "0");
  return p(d.getHours()) + ":" + p(d.getMinutes());
}
function fmtStamp(t) {
  if (!t) return "—";
  const d = new Date(t * 1000);
  const p = (x) => String(x).padStart(2, "0");
  return `${d.getMonth() + 1}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}
function fmtDur(sec) {
  if (!isFinite(sec) || sec <= 0) return "—";
  sec = Math.round(sec);
  if (sec < 60) return sec + " 秒";
  if (sec < 3600) return Math.floor(sec / 60) + " 分 " + (sec % 60) + " 秒";
  return Math.floor(sec / 3600) + " 小时 " + Math.floor((sec % 3600) / 60) + " 分";
}
function stateLabel(s) {
  return ({ pending: "待确认", connecting: "连接中", waiting: "等待对方", active: "传输中",
    done: "已完成", failed: "失败", rejected: "已拒绝", canceled: "已取消" })[s] || s || "—";
}
function stateClass(s) {
  if (s === "done") return "ok";
  if (s === "active" || s === "connecting") return "live";
  if (s === "pending" || s === "waiting") return "warn";
  if (s === "failed" || s === "rejected") return "bad";
  return "";
}
function trackClass(s) {
  if (s === "done") return "ok";
  if (s === "failed" || s === "rejected" || s === "canceled") return "bad";
  if (s === "pending" || s === "waiting") return "warn";
  return "";
}
function modeLabel(m) { return ({ lan: "局域网", direct: "公网直连", v6: "IPv6 直连", relay: "中继" })[m] || m || ""; }
function modeClass(m) { return m === "relay" ? "relay" : m ? "ok" : ""; }
function jobTitle(j) {
  const it = j.items || [];
  if (!it.length) return "传输任务";
  return it.length === 1 ? it[0].name : `${it[0].name} 等 ${it.length} 个文件`;
}
function jobPeer(j) { return j.peer_name || shortID(j.peer_id) || j.addr || "未知节点"; }
function pct(j) {
  if (j.state === "done") return 100;
  if (!j.total) return 0;
  return Math.max(0, Math.min(100, (j.done / j.total) * 100));
}

/* 文件类型着色 */
const FTYPES = [
  [/\.(png|jpe?g|gif|webp|heic|bmp|svg|avif|tiff?)$/i, "var(--violet)"],
  [/\.(mp4|mov|mkv|avi|webm|m4v|flv|wmv)$/i, "var(--rose)"],
  [/\.(mp3|wav|flac|aac|m4a|ogg|opus|aiff?)$/i, "var(--amber)"],
  [/\.(zip|rar|7z|tar|gz|bz2|xz|dmg|iso|pkg)$/i, "var(--sand)"],
  [/\.(pdf|docx?|xlsx?|pptx?|pages|numbers|key|txt|md|rtf|csv)$/i, "var(--beam)"],
  [/\.(go|js|ts|tsx|jsx|py|rs|c|h|cpp|java|kt|swift|sh|json|ya?ml|html|css|sql)$/i, "var(--signal)"],
];
function fileType(name) {
  const n = name || "";
  for (const [re, color] of FTYPES) if (re.test(n)) return color;
  return "var(--muted)";
}
function fileTag(name) {
  const m = /\.([a-z0-9]{1,5})$/i.exec(name || "");
  return m ? m[1].toUpperCase().slice(0, 4) : "FILE";
}
function ftypeNode(name, big) {
  const n = el("div", "ftype");
  n.style.color = fileType(name);
  if (big) { n.style.width = "46px"; n.style.height = "46px"; n.style.fontSize = "11px"; }
  n.append(el("span", null, fileTag(name)));
  return n;
}

/* 头像 */
const AV_GRADS = [
  "linear-gradient(135deg,#4C8DFF,#2EE6A8)", "linear-gradient(135deg,#A98BFF,#4C8DFF)",
  "linear-gradient(135deg,#FFB454,#FF6B81)", "linear-gradient(135deg,#2EE6A8,#4C8DFF)",
  "linear-gradient(135deg,#F0E2CE,#FFB454)", "linear-gradient(135deg,#FF6B81,#A98BFF)",
];
function hash(s) {
  let h = 2166136261;
  for (let i = 0; i < (s || "").length; i++) { h ^= s.charCodeAt(i); h = Math.imul(h, 16777619); }
  return Math.abs(h);
}
function initials(name) {
  const n = (name || "?").trim();
  const tail = /[-_ ]([A-Za-z0-9\u4e00-\u9fa5]{1,2})$/.exec(n);
  if (tail) return tail[1].toUpperCase();
  if (/^[\u4e00-\u9fa5]/.test(n)) return n.slice(0, 2);
  const parts = n.split(/[\s._\-]+/).filter(Boolean);
  if (parts.length > 1) return (parts[0][0] + parts[1][0]).toUpperCase();
  return n.slice(0, 2).toUpperCase();
}
function paintAvatar(node, seed, name) {
  node.style.background = AV_GRADS[hash(seed || name) % AV_GRADS.length];
  node.textContent = initials(name);
}
function avatarNode(seed, name, size) {
  const a = el("span", "avatar" + (size ? " " + size : ""));
  paintAvatar(a, seed, name);
  return a;
}

/* ── 提示音 ───────────────────────────────── */
const Sound = {
  on: localStorage.getItem("fsx.sound") !== "0",
  ctx: null,
  unlock() {
    if (!this.ctx) {
      const AC = window.AudioContext || window.webkitAudioContext;
      if (AC) this.ctx = new AC();
    }
    if (this.ctx && this.ctx.state === "suspended") this.ctx.resume();
  },
  play(freqs, dur = 0.13, gain = 0.05, type = "sine") {
    if (!this.on) return;
    this.unlock();
    const c = this.ctx;
    if (!c) return;
    freqs.forEach((f, i) => {
      const t = c.currentTime + i * dur * 0.85;
      const o = c.createOscillator(), g = c.createGain();
      o.type = type;
      o.frequency.setValueAtTime(f, t);
      g.gain.setValueAtTime(0, t);
      g.gain.linearRampToValueAtTime(gain, t + 0.012);
      g.gain.exponentialRampToValueAtTime(0.0001, t + dur);
      o.connect(g).connect(c.destination);
      o.start(t); o.stop(t + dur + 0.02);
    });
  },
  done() { this.play([660, 990], 0.15, 0.05); },
  ask() { this.play([520, 700], 0.16, 0.045, "triangle"); },
  bad() { this.play([320, 220], 0.18, 0.05, "sawtooth"); },
  msg() { this.play([880], 0.1, 0.035); },
};
function paintSound() {
  const b = $("#soundBtn");
  b.classList.toggle("on", Sound.on);
  b.title = Sound.on ? "提示音已开" : "提示音已关";
  b.replaceChildren(icon(Sound.on ? "sound" : "mute"));
}

/* ── 轻提示 ───────────────────────────────── */
function toast(msg, opt = {}) {
  const box = $("#toasts");
  const kind = opt.kind || "info";
  const t = el("div", "toast " + kind);
  const ti = el("span", "ti");
  ti.append(icon(kind === "ok" ? "check" : kind === "bad" ? "x" : "info"));
  t.append(ti, el("span", "tx", msg));
  let timer = null;
  const close = () => {
    if (timer) clearTimeout(timer);
    t.classList.add("out");
    setTimeout(() => t.remove(), 220);
  };
  if (opt.action) {
    const a = el("button", "act", opt.action);
    a.type = "button";
    a.onclick = () => { close(); opt.onAction && opt.onAction(); };
    t.append(a);
  }
  t.append(iconBtn("x", "关闭", close, "xs"));
  box.append(t);
  while (box.children.length > 4) box.firstChild.remove();
  const ms = opt.duration || (opt.action ? 6000 : 2600);
  if (ms > 0) timer = setTimeout(close, ms);
  return close;
}

async function api(path, body) {
  const r = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-FSX": "1" },
    body: JSON.stringify(body || {}),
  });
  const j = await r.json().catch(() => ({}));
  if (!r.ok) {
    const e = new Error(j.error || "HTTP " + r.status);
    e.status = r.status;
    throw e;
  }
  return j;
}
function fail(e) {
  if (e && e.status === 401) { location.reload(); return; }
  toast((e && e.message) || String(e), { kind: "bad" });
}
async function copy(text, msg) {
  if (!text) return;
  try {
    await navigator.clipboard.writeText(text);
  } catch (_) {
    const ta = el("textarea");
    ta.value = text;
    ta.style.cssText = "position:fixed;opacity:0";
    document.body.append(ta);
    ta.select();
    try { document.execCommand("copy"); } catch (_) {}
    ta.remove();
  }
  toast(msg || "已复制", { kind: "ok" });
}

/* ── 键控列表同步 ─────────────────────────── */
function syncList(box, items, keyOf, create, update) {
  const old = new Map();
  for (const n of Array.from(box.children)) {
    if (n.dataset.key) old.set(n.dataset.key, n);
    else n.remove();
  }
  let prev = null;
  items.forEach((it, i) => {
    const k = String(keyOf(it, i));
    let node = old.get(k);
    if (node) {
      old.delete(k);
      const swapped = update(node, it);
      if (swapped && swapped !== node) {
        swapped.dataset.key = k;
        node.replaceWith(swapped);
        node = swapped;
      }
    }
    else {
      node = create(it);
      node.dataset.key = k;
      node.style.animationDelay = Math.min(i, 8) * 26 + "ms";
    }
    const ref = prev ? prev.nextSibling : box.firstChild;
    if (node !== ref) box.insertBefore(node, ref);
    prev = node;
  });
  for (const n of old.values()) n.remove();
}
function emptyState(ic, title, text) {
  const n = el("div", "empty");
  n.append(icon(ic), el("b", null, title), el("p", null, text));
  return n;
}
function setText(node, text) {
  if (node.textContent !== text) node.textContent = text;
}

/* ── 设备栏 ───────────────────────────────── */
function findPeer(key) { return (S.peers || []).find((p) => peerKey(p) === key) || null; }
function currentPeer() { return UI.target ? findPeer(UI.target) : null; }

function setTarget(key) {
  UI.target = key || null;
  const p = currentPeer();
  const t = $("#dzTarget");
  t.classList.toggle("set", !!p);
  setText(t, p ? "发送给 " + (p.name || shortID(p.id) || p.addr) : "未选择设备 · 点击左侧设备选定");
  setText($("#veilTitle"), p ? "松手发送给 " + (p.name || shortID(p.id)) : "松手后选择接收设备");
  $$(".device").forEach((n) => n.classList.toggle("sel", n.dataset.key === UI.target));
}

function renderPeers() {
  const box = $("#devices");
  const q = UI.peerQuery.trim().toLowerCase();
  let peers = (S.peers || []).slice();
  if (q) peers = peers.filter((p) => (p.name || "").toLowerCase().includes(q) || (p.id || "").toLowerCase().includes(q) || (p.addr || "").includes(q));
  peers.sort((a, b) => (b.online ? 1 : 0) - (a.online ? 1 : 0) || (a.name || "").localeCompare(b.name || "", "zh"));
  setText($("#peerCount"), String((S.peers || []).length));

  if (!peers.length) {
    box.replaceChildren();
    const r = el("div", "radar");
    r.append(el("div", "radar-dial"));
    r.append(el("p", null, q ? "没有匹配的设备" : "正在扫描局域网… 也可以直接输入对方的节点 ID 添加"));
    box.append(r);
    return;
  }
  syncList(box, peers, (p) => peerKey(p), deviceRow, updateDevice);
  setTarget(UI.target);
}

function deviceRow(p) {
  const n = el("div", "device");
  n.tabIndex = 0;
  const av = avatarNode(p.id || p.addr, p.name || p.addr);
  const body = el("div", "d-body");
  const name = el("div", "d-name");
  name.append(el("i", "beacon"), el("span", "nm"));
  body.append(name, el("div", "d-sub"));
  const acts = el("div", "d-acts");
  acts.append(
    iconBtn("send", "发送文件", () => pickFiles(findPeer(n.dataset.key)), "sm"),
    iconBtn("chat", "发消息", () => { setTab("messages"); openConversation(findPeer(n.dataset.key)); }, "sm"),
    iconBtn("info", "设备详情", () => openDrawer("peer", n.dataset.key), "sm"),
  );
  n.append(av, body, acts);
  n.onclick = () => setTarget(UI.target === n.dataset.key ? null : n.dataset.key);
  n.onkeydown = (e) => {
    if (e.key === "Enter" || e.key === " ") { e.preventDefault(); n.click(); }
  };
  n.ondragover = (e) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    e.stopPropagation();
    n.classList.add("dropping");
  };
  n.ondragleave = () => n.classList.remove("dropping");
  n.ondrop = (e) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    e.stopPropagation();
    n.classList.remove("dropping");
    hideVeil();
    const files = Array.from(e.dataTransfer.files || []);
    const peer = findPeer(n.dataset.key);
    if (files.length && peer) { setTarget(n.dataset.key); upload(peer, files); }
  };
  updateDevice(n, p);
  return n;
}
function updateDevice(n, p) {
  n.classList.toggle("off", !p.online);
  n.classList.toggle("sel", peerKey(p) === UI.target);
  paintAvatar(n.firstChild, p.id || p.addr, p.name || p.addr);
  const name = $(".d-name", n);
  $(".beacon", name).className = "beacon" + (p.online ? " on" : "");
  setText($(".nm", name), p.name || shortID(p.id) || p.addr || "未知设备");
  const sub = p.id ? shortID(p.id) + (p.addr ? " · " + p.addr : " · 经中继") : (p.addr || "");
  setText($(".d-sub", n), sub);
}

/* ── 传输列表 ─────────────────────────────── */
function visibleJobs() {
  let js = (S.jobs || []).filter((j) => !UI.hidden.has(j.id));
  if (UI.filter === "live") js = js.filter((j) => RUNNING.includes(j.state));
  else if (UI.filter === "pending") js = js.filter((j) => j.state === "pending");
  else if (UI.filter === "done") js = js.filter((j) => !LIVE.includes(j.state));
  const q = UI.query.trim().toLowerCase();
  if (q) {
    js = js.filter((j) => jobTitle(j).toLowerCase().includes(q) || jobPeer(j).toLowerCase().includes(q)
      || (j.items || []).some((i) => (i.name || "").toLowerCase().includes(q)));
  }
  const rank = (j) => j.state === "pending" ? 0 : RUNNING.includes(j.state) ? 1 : 2;
  return js.sort((a, b) => {
    const r = rank(a) - rank(b);
    if (r) return r;
    if (UI.sort === "size") return (b.total || 0) - (a.total || 0);
    if (UI.sort === "name") return jobTitle(a).localeCompare(jobTitle(b), "zh");
    return (b.updated || b.created || 0) - (a.updated || a.created || 0);
  });
}

function renderJobs() {
  const box = $("#jobs");
  const js = visibleJobs();
  $("#dropzone").classList.toggle("compact", (S.jobs || []).length > 0);
  $("#clearDone").disabled = !(S.jobs || []).some((j) => !LIVE.includes(j.state) && !UI.hidden.has(j.id));
  setText($("#tabLive"), String((S.jobs || []).filter((j) => LIVE.includes(j.state)).length));

  if (!js.length) {
    box.replaceChildren();
    const map = {
      all: ["send", "还没有传输任务", "把文件拖进来，或点「选择文件」。同一局域网会自动发现设备，跨网可输入节点 ID。"],
      live: ["bolt", "当前没有进行中的传输", "发起一次发送后，这里会显示实时速率与剩余时间。"],
      pending: ["lock", "没有待确认的接收请求", "别人向你发送文件时，会在这里出现核对码与文件清单。"],
      done: ["check", "没有已完成的记录", "完成的传输会保留在这里，可随时查看明细。"],
    };
    const [ic, t, p] = map[UI.filter] || map.all;
    box.append(emptyState(ic, UI.query ? "没有匹配的任务" : t, UI.query ? "换个关键词试试，或清除搜索。" : p));
    return;
  }
  syncList(box, js, (j) => j.id,
    (j) => j.state === "pending" ? askCard(j) : jobRow(j),
    (node, j) => {
      if (j.state === "pending" && node.classList.contains("ask")) { updateAsk(node, j); return node; }
      if (j.state !== "pending" && node.classList.contains("job")) { updateJob(node, j); return node; }
      return j.state === "pending" ? askCard(j) : jobRow(j);
    });
}

function jobRow(j) {
  const n = el("div", "job");
  const ft = ftypeNode((j.items && j.items[0] && j.items[0].name) || "");
  const body = el("div", "j-body");
  const top = el("div", "j-top");
  const dir = el("span", "dirglyph");
  dir.append(icon("send"));
  top.append(dir, el("span", "j-name"), el("span", "badge st"));
  const meta = el("div", "j-meta");
  meta.append(el("span", "who"), el("span", "sep", "·"), el("span", "mono sz"));
  const sas = el("div", "j-sas");
  sas.append(el("span", "mono"), el("span", "j-sas-note", "告诉对方这 6 位数字，一致再确认"));
  const track = el("div", "track");
  track.append(el("i"));
  body.append(top, meta, sas, track);
  const right = el("div", "j-right");
  right.append(el("span", "j-pct"), el("div", "j-acts"));
  n.append(ft, body, right);
  n.onclick = () => openDrawer("job", j.id);
  updateJob(n, j);
  return n;
}

function updateJob(n, j) {
  const live = RUNNING.includes(j.state);
  n.className = "job" + (live ? " live" : "") + (j.state === "done" ? " done" : "")
    + (j.state === "failed" || j.state === "rejected" ? " bad" : "");
  const first = (j.items && j.items[0] && j.items[0].name) || "";
  const ft = $(".ftype", n);
  ft.style.color = fileType(first);
  setText($("span", ft), fileTag(first));
  const dir = $(".dirglyph", n);
  dir.className = "dirglyph " + (j.dir === "recv" ? "recv" : "send");
  dir.replaceChildren(icon(j.dir === "recv" ? "recv" : "send"));
  setText($(".j-name", n), jobTitle(j));
  const st = $(".badge.st", n);
  st.className = "badge st " + stateClass(j.state);
  setText(st, stateLabel(j.state));
  setText($(".who", n), (j.dir === "recv" ? "来自 " : "发给 ") + jobPeer(j));
  const sas = $(".j-sas", n);
  const hasSas = j.state === "waiting" && !!j.sas;
  sas.hidden = !hasSas;
  if (hasSas) setText($(".mono", sas), "核对码 " + j.sas);

  let info;
  if (j.state === "failed") info = j.error || "未知错误";
  else if (j.state === "rejected") info = "对方拒绝接收";
  else if (j.state === "canceled") info = "已取消 · " + fmtSize(j.done);
  else if (j.state === "done") info = fmtSize(j.total) + " · " + fmtClock(j.updated);
  else if (j.state === "pending") info = fmtSize(j.total) + " 待确认";
  else {
    info = fmtSize(j.done) + " / " + fmtSize(j.total);
    if (j.speed > 0) {
      info += " · " + fmtSpeed(j.speed);
      const left = (j.total - j.done) / j.speed;
      if (left > 1) info += " · 剩 " + fmtDur(left);
    }
  }
  setText($(".sz", n), info);

  const track = $(".track", n);
  track.className = "track " + trackClass(j.state);
  $("i", track).style.width = pct(j).toFixed(1) + "%";
  setText($(".j-pct", n), j.state === "done" ? "100%" : Math.floor(pct(j)) + "%");

  const acts = $(".j-acts", n);
  acts.replaceChildren();
  if (j.mode) acts.append(el("span", "badge " + modeClass(j.mode), modeLabel(j.mode)));
  if (live) acts.append(iconBtn("stop", "取消传输", () => cancelJob(j.id), "sm"));
  if (j.state === "done" && j.dir === "recv") {
    if (S.local) {
      acts.append(iconBtn("folder", "打开文件所在文件夹", () => revealJob(j.id), "sm"));
    } else {
      const a = el("a", "iconbtn sm");
      a.href = "/dl/" + j.id;
      a.title = (j.items || []).length > 1 ? "打包下载" : "下载";
      a.setAttribute("aria-label", a.title);
      a.append(icon("download"));
      a.onclick = (e) => e.stopPropagation();
      acts.append(a);
    }
  }
  acts.append(iconBtn("info", "查看详情", () => openDrawer("job", j.id), "sm"));
}

function askCard(j) {
  const n = el("div", "ask");
  const head = el("div", "ask-head");
  head.append(avatarNode(j.peer_id, jobPeer(j)));
  const hb = el("div");
  hb.append(el("div", "ask-title"), el("div", "ask-sub"));
  head.append(hb, el("span", "spacer"), el("span", "badge warn", "需要确认"));
  const body = el("div", "ask-body");
  const sas = el("div", "sas");
  sas.append(el("span", "sas-code"), el("span", "sas-note", "与发送方核对这 6 位数字一致再接收，可确认连接没有被中间人替换。"));
  body.append(sas);
  const foot = el("div", "ask-foot");
  const trust = el("label", "trust");
  const cb = el("input");
  cb.type = "checkbox";
  trust.append(cb, document.createTextNode("以后自动接收该设备"));
  trust.onclick = (e) => e.stopPropagation();
  foot.append(trust, el("span", "spacer"));
  foot.append(btn("接收", "ok sm", "check", () => decide(j.id, true, cb.checked)));
  foot.append(btn("拒绝", "ghost sm", "x", () => decide(j.id, false, false)));
  const files = el("div", "filelist scroll");
  const list = el("div", "ask-list");
  list.append(files);
  n.append(head, body, foot, list);
  updateAsk(n, j);
  return n;
}
function updateAsk(n, j) {
  const items = j.items || [];
  setText($(".ask-title", n), jobPeer(j) + " 想发送 " + items.length + " 个文件");
  setText($(".ask-sub", n), fmtSize(j.total) + (j.mode ? " · " + modeLabel(j.mode) : "") + " · " + fmtClock(j.created));
  setText($(".sas-code", n), j.sas || "······");
  const list = $(".filelist", n);
  const shown = items.slice(0, 40).map((it) => ({ id: it.id, name: it.name, size: it.size, more: false }));
  if (items.length > 40) shown.push({ id: "more", name: "…共 " + items.length + " 个文件", size: 0, more: true });
  syncList(list, shown, (it) => it.id,
    (it) => {
      const r = el("div", "filerow");
      if (!it.more) {
        const f = ftypeNode(it.name);
        f.style.width = "22px"; f.style.height = "22px"; f.style.borderRadius = "7px"; f.style.fontSize = "7.5px";
        r.append(f);
      }
      r.append(el("span", "nm", it.name), el("span", "sz", it.more ? "" : fmtSize(it.size)));
      return r;
    },
    (node, it) => {
      setText($(".nm", node), it.name);
      setText($(".sz", node), it.more ? "" : fmtSize(it.size));
    });
}

/* ── 收件箱 ───────────────────────────────── */
function renderInbox() {
  const box = $("#inbox");
  const q = UI.query.trim().toLowerCase();
  let js = (S.jobs || []).filter((j) => j.dir === "recv" && j.state === "done");
  setText($("#tabInbox"), String(js.length));
  if (q) js = js.filter((j) => (j.items || []).some((i) => (i.name || "").toLowerCase().includes(q)) || jobPeer(j).toLowerCase().includes(q));
  js = js.slice().sort((a, b) => (b.updated || 0) - (a.updated || 0));
  if (!js.length) {
    box.replaceChildren(emptyState("folder", q ? "没有匹配的文件" : "收件箱是空的",
      q ? "换个关键词试试。" : S.local
        ? "别人发来的文件会保存在接收目录，也会在这里列出，可直接打开所在文件夹。"
        : "别人发来的文件会保存在接收目录，也会在这里列出，可直接下载。"));
    return;
  }
  syncList(box, js, (j) => j.id, inboxRow, updateInbox);
}
function inboxRow(j) {
  const n = el("div", "job done");
  const body = el("div", "j-body");
  const top = el("div", "j-top");
  top.append(el("span", "j-name"), el("span", "badge ok", "已接收"));
  const meta = el("div", "j-meta");
  meta.append(el("span", "who"), el("span", "sep", "·"), el("span", "mono sz"));
  body.append(top, meta);
  const right = el("div", "j-right");
  right.append(el("div", "j-acts"));
  n.append(ftypeNode(""), body, right);
  n.onclick = () => openDrawer("job", j.id);
  updateInbox(n, j);
  return n;
}
function updateInbox(n, j) {
  const items = j.items || [];
  const first = (items[0] && items[0].name) || "";
  const ft = $(".ftype", n);
  ft.style.color = fileType(first);
  setText($("span", ft), items.length > 1 ? items.length + "个" : fileTag(first));
  setText($(".j-name", n), jobTitle(j));
  setText($(".who", n), "来自 " + jobPeer(j));
  setText($(".sz", n), fmtSize(j.total) + " · " + fmtStamp(j.updated));
  const acts = $(".j-acts", n);
  acts.replaceChildren();
  const link = (href, title, ic) => {
    const a = el("a", "iconbtn sm");
    a.href = href; a.title = title; a.setAttribute("aria-label", title);
    a.append(icon(ic));
    a.onclick = (e) => e.stopPropagation();
    return a;
  };
  if (S.local) {
    acts.append(iconBtn("folder", "打开文件所在文件夹", () => revealJob(j.id), "sm"));
  } else if (items.length > 1) {
    acts.append(link("/dl/" + j.id, "打包下载 ZIP", "folder"));
    acts.append(link("/dl/" + j.id + "?i=0", "下载第一个文件", "download"));
  } else {
    acts.append(link("/dl/" + j.id, "下载", "download"));
  }
  acts.append(iconBtn("info", "查看详情", () => openDrawer("job", j.id), "sm"));
}

/* ── 消息 ─────────────────────────────────── */
function msgKey(m) {
  if (m.dir === "out") return m.to_id || ("n:" + (m.to_name || "unknown"));
  return m.from_id || ("n:" + (m.from_name || "unknown"));
}
function msgName(m) {
  return m.dir === "out" ? (m.to_name || shortID(m.to_id) || "未知设备") : (m.from_name || shortID(m.from_id) || "未知设备");
}
function conversations() {
  const map = new Map();
  for (const p of S.peers || []) {
    const k = peerKey(p);
    if (!k) continue;
    map.set(k, { key: k, id: p.id || "", name: p.name || shortID(p.id) || p.addr, addr: p.addr || "", online: p.online, last: "", time: 0, count: 0 });
  }
  for (const m of S.messages || []) {
    const k = msgKey(m);
    let c = map.get(k);
    if (!c) {
      c = { key: k, id: m.dir === "out" ? (m.to_id || "") : (m.from_id || ""), name: msgName(m), addr: "", online: false, last: "", time: 0, count: 0 };
      map.set(k, c);
    }
    c.count++;
    if ((m.time || 0) >= c.time) { c.time = m.time || 0; c.last = m.body || ""; }
  }
  return Array.from(map.values()).sort((a, b) =>
    (b.time || 0) - (a.time || 0) || (b.online ? 1 : 0) - (a.online ? 1 : 0) || a.name.localeCompare(b.name, "zh"));
}
function convMessages(key) {
  const server = (S.messages || []).filter((m) => msgKey(m) === key)
    .map((m) => ({ dir: m.dir, body: m.body, time: m.time, pending: false }));
  const pend = UI.pendingOut.filter((p) => p.key === key)
    .map((p) => ({ dir: "out", body: p.body, time: p.time, pending: true }));
  return server.concat(pend);
}
function openConversation(p) {
  if (!p) return;
  UI.conv = { key: peerKey(p), id: p.id || "", name: p.name || shortID(p.id) || p.addr, addr: p.addr || "" };
  $("#view-messages").classList.add("thread-open");
  renderMessages();
  const b = $("#bubbles");
  b.scrollTop = b.scrollHeight;
  $("#msgInput").focus();
}
function renderMessages() {
  const convs = conversations();
  setText($("#tabMsgs"), String((S.messages || []).length));
  const box = $("#convs");
  if (!convs.length) {
    box.replaceChildren(emptyState("chat", "还没有会话", "左侧设备上线后，点设备上的消息图标即可开始加密文字对话。"));
  } else {
    syncList(box, convs, (c) => c.key, convRow, updateConv);
  }
  const c = UI.conv;
  const bub = $("#bubbles");
  if (!c) {
    setText($("#convName"), "选择一个设备");
    setText($("#convSub"), "端到端加密的文字消息");
    paintAvatar($("#convAvatar"), "none", "··");
    $("#convSend").hidden = true;
    $("#composer").hidden = true;
    bub.replaceChildren(emptyState("chat", "选择左侧会话", "消息与文件走同一条 TLS 1.3 通道，不经过任何第三方服务器保存。"));
    return;
  }
  $("#composer").hidden = false;
  $("#convSend").hidden = false;
  setText($("#convName"), c.name);
  setText($("#convSub"), c.id ? shortID(c.id) + (c.addr ? " · " + c.addr : " · 经中继") : (c.addr || ""));
  paintAvatar($("#convAvatar"), c.id || c.key, c.name);
  const msgs = convMessages(c.key);
  const nearBottom = bub.scrollHeight - bub.scrollTop - bub.clientHeight < 80;
  if (!msgs.length) {
    bub.replaceChildren(emptyState("chat", "还没有消息", "发一句话或一个文件试试，Enter 发送，Shift+Enter 换行。"));
    return;
  }
  syncList(bub, msgs, (m, i) => `${m.dir}|${m.time}|${i}`,
    (m) => {
      const n = el("div", "bubble " + (m.dir === "out" ? "out" : "in"));
      n.append(el("span", "bd"), el("span", "t"));
      updateBubble(n, m);
      return n;
    },
    updateBubble);
  if (nearBottom) bub.scrollTop = bub.scrollHeight;
}
function updateBubble(node, m) {
  setText($(".bd", node), m.body);
  setText($(".t", node), fmtClock(m.time) + (m.pending ? " · 发送中" : ""));
  node.classList.toggle("pending", !!m.pending);
  return node;
}
function convRow(c) {
  const n = el("button", "conv");
  n.type = "button";
  n.append(avatarNode(c.id || c.key, c.name, "sm"));
  const body = el("div", "conv-body");
  body.append(el("div", "conv-name"), el("div", "conv-last"));
  n.append(body, el("i", "beacon"));
  n.onclick = () => openConversation(c);
  updateConv(n, c);
  return n;
}
function updateConv(n, c) {
  n.classList.toggle("sel", !!UI.conv && UI.conv.key === c.key);
  paintAvatar(n.firstChild, c.id || c.key, c.name);
  setText($(".conv-name", n), c.name);
  setText($(".conv-last", n), c.count ? c.last : (c.online ? "在线 · 点击开始对话" : "离线"));
  $(".beacon", n).className = "beacon" + (c.online ? " on" : "");
}

/* ── 遥测 ─────────────────────────────────── */
const spark = { data: new Array(80).fill(0), shown: new Array(80).fill(0), dirty: true };
function renderMetrics() {
  const jobs = S.jobs || [];
  const active = jobs.filter((j) => RUNNING.includes(j.state));
  const sent = jobs.filter((j) => j.dir === "send");
  const recv = jobs.filter((j) => j.dir === "recv");
  const sum = (arr) => arr.reduce((a, j) => a + (j.done || 0), 0);
  const speed = active.reduce((a, j) => a + (j.speed || 0), 0);
  if (speed > UI.peak) UI.peak = speed;
  setText($("#mActive"), String(active.length));
  setText($("#mSent"), fmtSize(sum(sent)));
  setText($("#mSentN"), sent.length + " 个任务");
  setText($("#mRecv"), fmtSize(sum(recv)));
  setText($("#mRecvN"), recv.length + " 个任务");
  setText($("#mSpeed"), fmtSpeed(speed));
  setText($("#sbPeak"), "峰值 " + fmtSpeed(UI.peak));
  spark.data.push(speed);
  spark.data.shift();
  spark.dirty = true;
}
function drawSpark() {
  const cv = $("#spark");
  const w = cv.clientWidth, h = cv.clientHeight || 34;
  if (!w) return;
  const dpr = Math.min(window.devicePixelRatio || 1, 2);
  if (cv.width !== Math.round(w * dpr) || cv.height !== Math.round(h * dpr)) {
    cv.width = Math.round(w * dpr);
    cv.height = Math.round(h * dpr);
  }
  const g = cv.getContext("2d");
  if (!g) return;
  g.setTransform(dpr, 0, 0, dpr, 0, 0);
  g.clearRect(0, 0, w, h);
  const d = spark.shown;
  const max = Math.max(1024, ...d);
  const pad = 4;
  const pt = (i) => [(i / (d.length - 1)) * w, h - pad - (d[i] / max) * (h - pad * 2)];
  g.beginPath();
  g.moveTo(...pt(0));
  for (let i = 1; i < d.length; i++) {
    const [x0, y0] = pt(i - 1), [x1, y1] = pt(i);
    const mx = (x0 + x1) / 2;
    g.bezierCurveTo(mx, y0, mx, y1, x1, y1);
  }
  const line = g.createLinearGradient(0, 0, w, 0);
  line.addColorStop(0, "rgba(76,141,255,.35)");
  line.addColorStop(0.6, "rgba(76,141,255,.95)");
  line.addColorStop(1, "rgba(46,230,168,1)");
  g.strokeStyle = line;
  g.lineWidth = 1.6;
  g.lineJoin = "round";
  g.stroke();
  g.lineTo(w, h);
  g.lineTo(0, h);
  g.closePath();
  const fill = g.createLinearGradient(0, 0, 0, h);
  fill.addColorStop(0, "rgba(46,230,168,.22)");
  fill.addColorStop(1, "rgba(76,141,255,0)");
  g.fillStyle = fill;
  g.fill();
  const [lx, ly] = pt(d.length - 1);
  if (d[d.length - 1] > 0) {
    g.beginPath();
    g.arc(lx - 1.5, ly, 2.4, 0, Math.PI * 2);
    g.fillStyle = "#2EE6A8";
    g.shadowColor = "rgba(46,230,168,.9)";
    g.shadowBlur = 8;
    g.fill();
    g.shadowBlur = 0;
  }
}
function sparkLoop() {
  let moving = false;
  for (let i = 0; i < spark.shown.length; i++) {
    const diff = spark.data[i] - spark.shown[i];
    if (Math.abs(diff) > 1) { spark.shown[i] += diff * 0.16; moving = true; }
    else spark.shown[i] = spark.data[i];
  }
  if (moving || spark.dirty) { drawSpark(); spark.dirty = false; }
  requestAnimationFrame(sparkLoop);
}

/* ── 详情抽屉 ─────────────────────────────── */
function openDrawer(kind, key) {
  UI.drawer = { kind, key };
  $("#drawer").hidden = false;
  $("#scrim").hidden = false;
  renderDrawer();
}
function closeDrawer() {
  UI.drawer = null;
  $("#drawer").hidden = true;
  if ($("#modal").hidden) $("#scrim").hidden = true;
  $("#drawerBody").replaceChildren();
}
function kv(rows) {
  const d = el("dl", "kv");
  for (const [k, v, mono] of rows) {
    if (v == null || v === "") continue;
    d.append(el("dt", null, k), el("dd", mono ? "mono" : null, String(v)));
  }
  return d;
}
function renderDrawer() {
  if (!UI.drawer) return;
  const body = $("#drawerBody");
  const top = body.scrollTop;
  const { kind, key } = UI.drawer;
  if (kind === "job") {
    const j = (S.jobs || []).find((x) => x.id === key);
    if (!j) { closeDrawer(); return; }
    setText($("#drawerKind"), j.dir === "recv" ? "接收详情" : "发送详情");
    const items = j.items || [];
    const hero = el("div", "dt-hero");
    hero.append(ftypeNode((items[0] && items[0].name) || "", true));
    const hb = el("div");
    hb.append(el("div", "dt-title", jobTitle(j)),
      el("div", "dt-sub", stateLabel(j.state) + " · " + (j.dir === "recv" ? "来自 " : "发给 ") + jobPeer(j)));
    hero.append(hb);
    const track = el("div", "track " + trackClass(j.state));
    const fill = el("i");
    fill.style.width = pct(j).toFixed(1) + "%";
    track.append(fill);
    const acts = el("div", "dt-actions");
    if (RUNNING.includes(j.state)) acts.append(btn("取消传输", "danger sm", "stop", () => cancelJob(j.id)));
    if (j.state === "pending") {
      acts.append(btn("接收", "ok sm", "check", () => decide(j.id, true, false)));
      acts.append(btn("拒绝", "ghost sm", "x", () => decide(j.id, false, false)));
    }
    if (j.state === "done" && j.dir === "recv") {
      if (S.local) {
        acts.append(btn("打开文件所在文件夹", "solid sm", "folder", () => revealJob(j.id)));
      } else {
        const a = el("a", "btn solid sm");
        a.href = "/dl/" + j.id;
        a.append(icon("download"), document.createTextNode(items.length > 1 ? "打包下载" : "下载文件"));
        acts.append(a);
      }
    }
    acts.append(btn("复制任务 ID", "ghost sm", "copy", () => copy(j.id, "已复制任务 ID")));
    const sec = el("div", "dt-section");
    sec.append(el("span", "micro", "文件清单 · " + items.length));
    const list = el("div", "filelist");
    for (const it of items) {
      const r = el("div", "filerow");
      const p = it.size ? Math.min(100, (it.done / it.size) * 100) : 0;
      r.append(el("span", "nm", it.name),
        el("span", "sz", fmtSize(it.done) + " / " + fmtSize(it.size) + " · " + Math.floor(p) + "%"));
      list.append(r);
    }
    sec.append(list);
    body.replaceChildren(hero, track, kv([
      ["状态", stateLabel(j.state)],
      ["通道", modeLabel(j.mode), true],
      ["对端", jobPeer(j)],
      ["节点 ID", j.peer_id, true],
      ["地址", j.addr, true],
      ["总量", fmtSize(j.total), true],
      ["已完成", fmtSize(j.done), true],
      ["速率", fmtSpeed(j.speed), true],
      ["核对码", j.sas, true],
      ["开始", fmtStamp(j.created), true],
      ["更新", fmtStamp(j.updated), true],
      ["错误", j.error],
    ]), acts, sec);
  } else {
    const p = findPeer(key);
    if (!p) { closeDrawer(); return; }
    setText($("#drawerKind"), "设备详情");
    const hero = el("div", "dt-hero");
    hero.append(avatarNode(p.id || p.addr, p.name || p.addr, "lg"));
    const hb = el("div");
    hb.append(el("div", "dt-title", p.name || shortID(p.id) || p.addr),
      el("div", "dt-sub", (p.online ? "在线" : "离线") + (p.manual ? " · 手动添加" : " · 自动发现")));
    hero.append(hb);
    const acts = el("div", "dt-actions");
    acts.append(btn("发送文件", "solid sm", "send", () => pickFiles(p)));
    acts.append(btn("发消息", "ghost sm", "chat", () => { closeDrawer(); setTab("messages"); openConversation(p); }));
    if (p.id) acts.append(btn("复制节点 ID", "ghost sm", "copy", () => copy(p.id, "已复制节点 ID")));
    if ((p.v6 || []).length) acts.append(btn("检测 IPv6 直通", "ghost sm", "bolt", () => probeV6(p)));
    acts.append(btn(UI.target === key ? "取消默认设备" : "设为默认设备", "ghost sm", "check", () => {
      setTarget(UI.target === key ? null : key);
      renderDrawer();
    }));
    const related = (S.jobs || []).filter((j) => j.peer_id && j.peer_id === p.id).slice(-6).reverse();
    const sec = el("div", "dt-section");
    sec.append(el("span", "micro", "最近往来"));
    if (!related.length) sec.append(el("p", "hint", "还没有与这台设备的传输记录。"));
    for (const j of related) {
      const r = el("div", "filerow");
      r.style.cursor = "pointer";
      r.append(el("span", "nm", (j.dir === "recv" ? "↓ " : "↑ ") + jobTitle(j)),
        el("span", "sz", stateLabel(j.state) + " · " + fmtSize(j.total)));
      r.onclick = () => openDrawer("job", j.id);
      sec.append(r);
    }
    body.replaceChildren(hero, kv([
      ["节点 ID", p.id, true],
      ["地址", p.addr, true],
      ["IPv6", (p.v6 || []).length ? (p.v6[0] + ((p.v6 || []).length > 1 ? ` 等 ${(p.v6 || []).length} 个` : "")) : "无", true],
      ["状态", p.online ? "在线" : "离线"],
      ["来源", p.manual ? "手动添加" : "局域网发现"],
      ["最近发现", p.last_seen ? fmtStamp(p.last_seen) : "", true],
    ]), acts, sec);
  }
  body.scrollTop = top;
}

/* ── 弹层 ─────────────────────────────────── */
function openSheet(build) {
  const sheet = $("#sheet");
  sheet.replaceChildren();
  build(sheet);
  $("#modal").hidden = false;
}
function closeSheet() {
  $("#modal").hidden = true;
  $("#sheet").replaceChildren();
  if (!UI.drawer) $("#scrim").hidden = true;
}
function sheetHead(sheet, title, lead) {
  sheet.append(el("h3", null, title));
  if (lead) sheet.append(el("p", "lead", lead));
}
function peerPicker(sheet, onPick) {
  const peers = (S.peers || []).slice().sort((a, b) => (b.online ? 1 : 0) - (a.online ? 1 : 0));
  if (!peers.length) {
    sheet.append(emptyState("desktop", "还没有可用设备", "等待局域网发现，或在左侧输入对方的节点 ID 添加。"));
    return;
  }
  const list = el("div", "picklist");
  for (const p of peers) {
    const b = el("button", "conv");
    b.type = "button";
    b.append(avatarNode(p.id || p.addr, p.name || p.addr, "sm"));
    const body = el("div", "conv-body");
    body.append(el("div", "conv-name", p.name || shortID(p.id) || p.addr),
      el("div", "conv-last", (p.id ? shortID(p.id) + " · " : "") + (p.addr || "经中继")));
    b.append(body, el("i", "beacon" + (p.online ? " on" : "")));
    b.onclick = () => { closeSheet(); onPick(p); };
    list.append(b);
  }
  sheet.append(list);
}
function choosePeer(files) {
  const target = currentPeer();
  if (target) { upload(target, files); return; }
  const online = (S.peers || []).filter((p) => p.online);
  if (online.length === 1) { setTarget(peerKey(online[0])); upload(online[0], files); return; }
  if (!online.length) { toast("没有在线设备，请先添加对方节点 ID", { kind: "bad" }); return; }
  openSheet((sheet) => {
    sheetHead(sheet, "发送到哪台设备？", files.length + " 个文件 · " + fmtSize(files.reduce((a, f) => a + f.size, 0)));
    peerPicker(sheet, (p) => { setTarget(peerKey(p)); upload(p, files); });
    const acts = el("div", "sheet-acts");
    acts.append(btn("取消", "ghost sm", null, closeSheet));
    sheet.append(acts);
  });
}
function showKeys() {
  openSheet((sheet) => {
    sheetHead(sheet, "快捷键", "输入框内不触发");
    const k = el("div", "keys");
    const rows = [["/", "聚焦搜索"], ["O", "选择文件发送"], ["1 / 2 / 3", "传输 / 收件箱 / 消息"],
      ["?", "打开本帮助"], ["Esc", "关闭弹层与详情"], ["Enter", "发送消息"], ["Shift+Enter", "消息换行"]];
    for (const [key, desc] of rows) k.append(el("kbd", null, key), el("span", null, desc));
    sheet.append(k);
    const acts = el("div", "sheet-acts");
    acts.append(btn("知道了", "solid sm", "check", closeSheet));
    sheet.append(acts);
  });
}

/* ── 动作 ─────────────────────────────────── */
async function decide(id, accept, always) {
  try {
    await api("/api/decision", { id, accept, always: !!always });
    toast(accept ? "已接收，开始传输" : "已拒绝", { kind: accept ? "ok" : "info" });
  } catch (e) { fail(e); }
}
async function revealJob(id, i) {
  try { await api("/api/reveal", { id, i: i || 0 }); } catch (e) { fail(e); }
}
async function probeV6(p) {
  const port = (p.addr || "").split(":").pop();
  if (!port || !(p.v6 || []).length) { toast("该设备没有可探测的 IPv6 地址", { kind: "info" }); return; }
  try {
    const r = await api("/api/probe", { addr: `[${p.v6[0]}]:${port}` });
    toast(r.ok ? "IPv6 直通可用，发送时将优先走 IPv6" : "IPv6 不通，将回落局域网/中继", { kind: r.ok ? "ok" : "info" });
  } catch (e) { fail(e); }
}
async function cancelJob(id) {
  try { await api("/api/cancel", { id }); toast("已取消传输"); } catch (e) { fail(e); }
}
function pickFiles(peer) {
  const p = peer || currentPeer();
  if (p) { UI.pickTarget = p; $("#fileInput").click(); return; }
  const online = (S.peers || []).filter((x) => x.online);
  if (!online.length) { toast("先在左侧添加或选择一台设备", { kind: "bad" }); return; }
  openSheet((sheet) => {
    sheetHead(sheet, "先选择接收设备", "选好后会打开文件选择器");
    peerPicker(sheet, (x) => { setTarget(peerKey(x)); UI.pickTarget = x; $("#fileInput").click(); });
    const acts = el("div", "sheet-acts");
    acts.append(btn("取消", "ghost sm", null, closeSheet));
    sheet.append(acts);
  });
}
function upload(peer, files) {
  if (!peer || !files.length) return;
  if (UI.upload) { toast("已有上传进行中，请稍候", { kind: "bad" }); return; }
  setTab("transfers");
  const fd = new FormData();
  if (peer.id) fd.append("peer", peer.id);
  if (peer.addr) fd.append("addr", peer.addr);
  for (const f of files) fd.append("files", f, f.name);
  const total = files.reduce((a, f) => a + f.size, 0);
  const label = files.length === 1 ? files[0].name : files.length + " 个文件 · " + fmtSize(total);

  const strip = $("#uploadStrip");
  strip.hidden = false;
  setText($("#upName"), label);
  setText($("#upPct"), "0%");
  $("#upFill").style.width = "0%";
  setText($("#upSub"), "正在交给本机节点 · " + (peer.name || shortID(peer.id) || peer.addr));

  const xhr = new XMLHttpRequest();
  UI.upload = { xhr };
  xhr.open("POST", "/api/upload");
  xhr.setRequestHeader("X-FSX", "1");
  xhr.upload.onprogress = (e) => {
    if (!e.lengthComputable) return;
    const p = Math.round((e.loaded / e.total) * 100);
    setText($("#upPct"), p + "%");
    $("#upFill").style.width = p + "%";
    setText($("#upSub"), fmtSize(e.loaded) + " / " + fmtSize(e.total) + " · 交给本机节点后再直连发送");
  };
  const finish = () => { UI.upload = null; strip.hidden = true; };
  xhr.onload = () => {
    finish();
    if (xhr.status === 200) {
      Sound.done();
      toast("已开始发送给 " + (peer.name || shortID(peer.id)), { kind: "ok" });
    } else if (xhr.status === 401) {
      location.reload();
    } else {
      let msg = "上传失败";
      try { msg = JSON.parse(xhr.responseText).error || msg; } catch (_) {}
      Sound.bad();
      toast(msg, { kind: "bad" });
    }
  };
  xhr.onerror = () => { finish(); Sound.bad(); toast("上传失败：网络错误", { kind: "bad" }); };
  xhr.onabort = () => { finish(); toast("已取消上传"); };
  xhr.send(fd);
}
async function sendText() {
  const c = UI.conv;
  const input = $("#msgInput");
  const body = input.value.trim();
  if (!c || !body) return;
  input.value = "";
  input.style.height = "auto";
  const pend = { key: c.key, body, time: Math.floor(Date.now() / 1000) };
  UI.pendingOut.push(pend);
  renderMessages();
  $("#bubbles").scrollTop = $("#bubbles").scrollHeight;
  try {
    await api("/api/text", { peer: c.id || "", addr: c.addr || "", body });
  } catch (e) {
    UI.pendingOut = UI.pendingOut.filter((p) => p !== pend);
    input.value = body;
    renderMessages();
    fail(e);
  }
}

/* ── 拖放 ─────────────────────────────────── */
function hasFiles(e) {
  const t = e.dataTransfer && e.dataTransfer.types;
  return !!t && Array.from(t).includes("Files");
}
function hideVeil() {
  UI.dragDepth = 0;
  $("#veil").hidden = true;
  $$(".device.dropping").forEach((n) => n.classList.remove("dropping"));
}
function initDrag() {
  window.addEventListener("dragenter", (e) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    UI.dragDepth++;
    $("#veil").hidden = false;
  });
  window.addEventListener("dragover", (e) => { if (hasFiles(e)) e.preventDefault(); });
  window.addEventListener("dragleave", (e) => {
    if (!hasFiles(e)) return;
    UI.dragDepth--;
    if (UI.dragDepth <= 0) hideVeil();
  });
  window.addEventListener("drop", (e) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    hideVeil();
    const files = Array.from(e.dataTransfer.files || []);
    if (files.length) choosePeer(files);
  });
  window.addEventListener("dragend", hideVeil);
}

/* ── 视图切换 ─────────────────────────────── */
function setTab(tab) {
  UI.tab = tab;
  $$(".tab").forEach((b) => b.classList.toggle("active", b.dataset.tab === tab));
  ["transfers", "inbox", "messages"].forEach((t) => { $("#view-" + t).hidden = t !== tab; });
  const msgs = tab === "messages";
  $("#searchWrap").hidden = msgs;
  $(".selectwrap").hidden = msgs;
  $("#search").placeholder = tab === "inbox" ? "搜索收到的文件…  /" : "搜索文件、设备…  /";
  if (!msgs) $("#view-messages").classList.remove("thread-open");
  spark.dirty = true;
}
function paintFilters() {
  $$("#jobFilters .chip").forEach((c) => c.classList.toggle("active", c.dataset.filter === UI.filter));
}

/* ── 事件差量：提示音与通知 ───────────────── */
let prevJobs = null, prevMsgCount = 0;
function detectEvents() {
  const jobs = S.jobs || [];
  if (prevJobs) {
    for (const j of jobs) {
      const was = prevJobs.get(j.id);
      if (!was) {
        if (j.state === "pending") {
          Sound.ask();
          toast(jobPeer(j) + " 想发送 " + (j.items || []).length + " 个文件", {
            kind: "info", action: "查看",
            onAction: () => { setTab("transfers"); UI.filter = "pending"; paintFilters(); renderJobs(); },
          });
        }
        continue;
      }
      if (was === j.state) continue;
      if (j.state === "done") {
        Sound.done();
        toast((j.dir === "recv" ? "已收到 " : "已发送 ") + jobTitle(j), {
          kind: "ok", action: j.dir === "recv" ? "打开收件箱" : null, onAction: () => setTab("inbox"),
        });
      } else if (j.state === "failed") {
        Sound.bad();
        toast("传输失败：" + (j.error || "未知错误"), { kind: "bad" });
      } else if (j.state === "rejected") {
        Sound.bad();
        toast(jobPeer(j) + " 拒绝了接收", { kind: "bad" });
      }
    }
  }
  prevJobs = new Map(jobs.map((j) => [j.id, j.state]));

  const msgs = S.messages || [];
  if (msgs.length > prevMsgCount && prevMsgCount > 0) {
    const last = msgs[msgs.length - 1];
    if (last && last.dir === "in") {
      Sound.msg();
      toast((last.from_name || shortID(last.from_id)) + "：" + (last.body || "").slice(0, 40), {
        kind: "info", action: "回复",
        onAction: () => {
          setTab("messages");
          openConversation(findPeer(last.from_id) || { id: last.from_id, name: last.from_name, addr: "" });
        },
      });
    }
  }
  prevMsgCount = msgs.length;
  UI.pendingOut = UI.pendingOut.filter((p) => !msgs.some((m) =>
    m.dir === "out" && msgKey(m) === p.key && m.body === p.body && (m.time || 0) >= p.time - 2));
}

/* ── 主渲染 ───────────────────────────────── */
function render() {
  const self = S.self || {};
  setText($("#ver"), self.version ? "v" + self.version : "—");
  setText($("#meName"), self.name || "正在连接本机节点…");
  setText($("#meIDText"), self.short || shortID(self.id) || "—");
  paintAvatar($("#meAvatar"), self.id, self.name || "fsx");
  setText($("#dlDir"), self.download_dir || "—");
  setText($("#sbPorts"), self.p2p_port ? `P2P :${self.p2p_port} · 控制台 :${self.http_port}` : "—");
  const v6 = self.v6 || [];
  const v6Ready = v6.length > 0 && !!self.p2p_port;
  setText($("#sbV6Text"), v6.length ? `IPv6 就绪 · ${v6.length} 个全局地址` : "IPv6 无全局地址");
  $("#sbV6").classList.toggle("off", !v6Ready);
  $("#sbV6Icon").hidden = !v6Ready;
  $("#sbV6").setAttribute("aria-label", v6Ready ? `本机 IPv6 直连地址，共 ${v6.length} 个全局地址，点击复制` : "本机 IPv6 状态");
  if (!$("#v6pop").hidden) buildV6Pop();

  const relayText = { online: "中继 已连接", connecting: "中继 连接中", offline: "中继 离线", off: "中继 未配置 · 仅局域网" };
  setText($("#relayText"), relayText[S.relay] || "中继 " + S.relay);
  $("#relayPill .beacon").className = "beacon" +
    (S.relay === "online" ? " on" : S.relay === "connecting" ? " warn" : S.relay === "offline" ? " bad" : "");

  if (S.code) {
    $("#pairCard").hidden = false;
    setText($("#pairCode"), S.code);
    setText($("#lanURL"), (self.lan || location.hostname) + ":" + (self.http_port || location.port || "9271"));
  } else {
    $("#pairCard").hidden = true;
  }

  detectEvents();
  renderPeers();
  renderJobs();
  renderInbox();
  renderMessages();
  renderMetrics();
  renderDrawer();
}

/* ── 本机 IPv6 浮层 ───────────────────────── */
let v6HideTimer = null;
function buildV6Pop() {
  const pop = $("#v6pop");
  const s = S.self || {}, v6 = s.v6 || [];
  pop.textContent = "";
  pop.append(el("div", "v6pop-head micro", v6.length ? `本机 IPv6 · ${v6.length} 个全局地址` : "本机 IPv6"));
  if (!v6.length) { pop.append(el("div", "v6pop-empty", "本机没有全局 IPv6 地址")); return; }
  if (s.p2p_port) {
    const dial = `[${v6[0]}]:${s.p2p_port}`;
    const row = el("div", "v6pop-row primary");
    const txt = el("div", "v6pop-txt");
    txt.append(el("span", "v6pop-label", "直连地址 · 推荐分享"), el("span", "mono", dial));
    row.append(txt, iconBtn("copy", "复制直连地址", () => copy(dial, "已复制 IPv6 直连地址，对方在「添加设备」粘贴即可"), "xs"));
    pop.append(row);
  }
  for (const a of v6) {
    const row = el("div", "v6pop-row");
    row.append(el("span", "mono", a), iconBtn("copy", "复制该地址", () => copy(a, "已复制 IPv6 地址"), "xs"));
    pop.append(row);
  }
  pop.append(el("div", "v6pop-hint", "对方在「添加设备」粘贴直连地址即可跨网直连"));
}
function showV6Pop() {
  cancelHideV6();
  const pop = $("#v6pop");
  buildV6Pop();
  pop.hidden = false;
  const r = $("#sbV6").getBoundingClientRect();
  pop.style.left = Math.max(8, Math.min(r.left, window.innerWidth - pop.offsetWidth - 8)) + "px";
  pop.style.bottom = (window.innerHeight - r.top + 8) + "px";
}
function hideV6Pop() { $("#v6pop").hidden = true; }
function scheduleHideV6() {
  if (v6HideTimer) clearTimeout(v6HideTimer);
  v6HideTimer = setTimeout(hideV6Pop, 160);
}
function cancelHideV6() { if (v6HideTimer) { clearTimeout(v6HideTimer); v6HideTimer = null; } }

/* ── 事件绑定 ─────────────────────────────── */
function initEvents() {
  $("#meID").onclick = () => copy(S.self && S.self.id, "已复制完整节点 ID，发给对方即可添加");
  $("#copyCode").onclick = () => copy(S.code, "已复制配对码");
  $("#dlDir").onclick = () => copy(S.self && S.self.download_dir, "已复制接收目录");
  const copyV6 = () => {
    const s = S.self || {}, v6 = s.v6 || [];
    if (!v6.length || !s.p2p_port) { toast("本机没有全局 IPv6 地址", { kind: "info" }); return; }
    copy(`[${v6[0]}]:${s.p2p_port}`, v6.length > 1
      ? `已复制 IPv6 直连地址（首个，共 ${v6.length} 个），对方在「添加设备」粘贴即可`
      : "已复制 IPv6 直连地址，对方在「添加设备」粘贴即可");
  };
  const v6El = $("#sbV6"), v6Pop = $("#v6pop");
  v6El.onclick = () => { copyV6(); showV6Pop(); };
  v6El.onkeydown = (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); copyV6(); showV6Pop(); } };
  v6El.onmouseenter = showV6Pop;
  v6El.onmouseleave = scheduleHideV6;
  v6El.onfocus = showV6Pop;
  v6El.onblur = scheduleHideV6;
  v6Pop.onmouseenter = cancelHideV6;
  v6Pop.onmouseleave = scheduleHideV6;
  v6Pop.onfocusin = cancelHideV6;
  document.addEventListener("click", (e) => {
    if (!v6Pop.hidden && !v6Pop.contains(e.target) && !v6El.contains(e.target)) hideV6Pop();
  });
  document.addEventListener("keydown", (e) => { if (e.key === "Escape") hideV6Pop(); });

  $("#peerSearchToggle").onclick = () => {
    const w = $("#peerSearchWrap");
    w.hidden = !w.hidden;
    $("#peerSearchToggle").classList.toggle("act", !w.hidden);
    if (!w.hidden) $("#peerSearch").focus();
    else { $("#peerSearch").value = ""; UI.peerQuery = ""; renderPeers(); }
  };
  $("#peerSearch").oninput = (e) => { UI.peerQuery = e.target.value; renderPeers(); };

  $("#addForm").onsubmit = async (e) => {
    e.preventDefault();
    const input = $("#manualAddr");
    const v = input.value.trim();
    if (!v) return;
    const b = $("#addPeer");
    b.disabled = true;
    try {
      const r = await api("/api/peer", v.includes(":") ? { addr: v } : { id: v });
      input.value = "";
      toast(r && r.name ? "已添加 " + r.name : "已添加设备", { kind: "ok" });
      const key = (r && r.id) || (r && r.addr) || "";
      if (key) setTarget(key);
    } catch (err) { fail(err); }
    b.disabled = false;
  };

  $$(".tab").forEach((t) => { t.onclick = () => setTab(t.dataset.tab); });
  $("#search").oninput = (e) => {
    UI.query = e.target.value;
    $("#searchClear").hidden = !UI.query;
    renderJobs(); renderInbox();
  };
  $("#searchClear").onclick = () => {
    $("#search").value = ""; UI.query = ""; $("#searchClear").hidden = true;
    renderJobs(); renderInbox(); $("#search").focus();
  };
  $("#sort").onchange = (e) => { UI.sort = e.target.value; renderJobs(); };
  $("#soundBtn").onclick = () => {
    Sound.on = !Sound.on;
    localStorage.setItem("fsx.sound", Sound.on ? "1" : "0");
    paintSound();
    if (Sound.on) { Sound.unlock(); Sound.msg(); }
  };
  $("#keysBtn").onclick = showKeys;

  $("#pickBtn").onclick = () => pickFiles(null);
  $$("#jobFilters .chip").forEach((c) => {
    c.onclick = () => { UI.filter = c.dataset.filter; paintFilters(); renderJobs(); };
  });
  $("#clearDone").onclick = () => {
    const ids = (S.jobs || []).filter((j) => !LIVE.includes(j.state)).map((j) => j.id);
    if (!ids.length) return;
    ids.forEach((id) => UI.hidden.add(id));
    renderJobs();
    toast("已从列表隐藏 " + ids.length + " 条记录（文件仍在磁盘上）", {
      action: "撤销", onAction: () => { ids.forEach((id) => UI.hidden.delete(id)); renderJobs(); },
    });
  };
  $("#upCancel").onclick = () => { if (UI.upload) UI.upload.xhr.abort(); };
  $("#fileInput").onchange = (e) => {
    const files = Array.from(e.target.files || []);
    e.target.value = "";
    if (!files.length) return;
    const p = UI.pickTarget;
    UI.pickTarget = null;
    if (p) upload(p, files); else choosePeer(files);
  };

  $("#convBack").onclick = () => $("#view-messages").classList.remove("thread-open");
  $("#convSend").onclick = () => {
    const c = UI.conv;
    if (!c) return;
    pickFiles(findPeer(c.key) || { id: c.id, name: c.name, addr: c.addr });
  };
  $("#composer").onsubmit = (e) => { e.preventDefault(); sendText(); };
  const mi = $("#msgInput");
  mi.onkeydown = (e) => {
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing) { e.preventDefault(); sendText(); }
  };
  mi.oninput = () => {
    mi.style.height = "auto";
    mi.style.height = Math.min(mi.scrollHeight, 120) + "px";
  };

  $("#drawerClose").onclick = closeDrawer;
  $("#scrim").onclick = () => { closeDrawer(); closeSheet(); };
  $("#modal").onclick = (e) => { if (e.target === $("#modal")) closeSheet(); };

  document.addEventListener("keydown", (e) => {
    const typing = /^(INPUT|TEXTAREA|SELECT)$/.test(e.target.tagName || "") || e.target.isContentEditable;
    if (e.key === "Escape") {
      if (!$("#modal").hidden) { closeSheet(); return; }
      if (UI.drawer) { closeDrawer(); return; }
      if (!$("#veil").hidden) { hideVeil(); return; }
      if (typing) e.target.blur();
      return;
    }
    if (typing || e.metaKey || e.ctrlKey || e.altKey) return;
    if (e.key === "/") { e.preventDefault(); if (UI.tab === "messages") setTab("transfers"); $("#search").focus(); }
    else if (e.key === "o" || e.key === "O") { e.preventDefault(); pickFiles(null); }
    else if (e.key === "1") setTab("transfers");
    else if (e.key === "2") setTab("inbox");
    else if (e.key === "3") setTab("messages");
    else if (e.key === "?") showKeys();
  });

  const unlock = () => Sound.unlock();
  window.addEventListener("pointerdown", unlock, { once: true });
  window.addEventListener("keydown", unlock, { once: true });
  window.addEventListener("resize", () => { spark.dirty = true; });
  if (window.ResizeObserver) new ResizeObserver(() => { spark.dirty = true; }).observe($("#metrics"));
  initDrag();
}

/* ── 连接 ─────────────────────────────────── */
function setConnected(ok) {
  if (UI.connected === ok) return;
  UI.connected = ok;
  $("#sbState").replaceChildren(el("i", "beacon" + (ok ? " on" : " bad")),
    document.createTextNode(ok ? "已连接本机节点" : "连接断开，正在重连…"));
}
function connect() {
  const es = new EventSource("/api/events");
  es.onopen = () => setConnected(true);
  es.onmessage = (e) => {
    try {
      const next = JSON.parse(e.data);
      next.peers = next.peers || [];
      next.jobs = next.jobs || [];
      next.messages = next.messages || [];
      S = next;
      setConnected(true);
      render();
    } catch (_) {}
  };
  es.onerror = () => {
    es.close();
    setConnected(false);
    setTimeout(connect, 2500);
  };
}

paintSound();
paintFilters();
initEvents();
setTarget(null);
setTab("transfers");
requestAnimationFrame(sparkLoop);
connect();
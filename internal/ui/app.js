const $ = (s, el = document) => el.querySelector(s);
let S = { self: {}, peers: [], jobs: [], messages: [], relay: "off" };
let pendingPeer = null;
let alwaysAccept = false;
let dragDepth = 0;
let toastTimer = null;

function elem(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text != null) e.textContent = text;
  return e;
}
function clear(node) {
  while (node.firstChild) node.removeChild(node.firstChild);
}
function shortID(id) {
  return id ? id.slice(0, 8) : "";
}
function fmtSize(n) {
  if (!n) return "0 B";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0, v = n;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return (v >= 100 ? Math.round(v) : v.toFixed(v < 10 ? 1 : 0)) + " " + u[i];
}
function fmtSpeed(n) { return n ? fmtSize(n) + "/s" : ""; }
function stateLabel(s) {
  return ({ connecting: "连接中", waiting: "待确认", active: "传输中", done: "已完成",
    failed: "失败", rejected: "已拒绝", canceled: "已取消", pending: "待接收" })[s] || s;
}
function modeLabel(m) {
  return ({ lan: "局域网直连", direct: "公网直连", relay: "中继转发" })[m] || m;
}
function toast(msg, ms = 2200) {
  const t = $("#toast");
  t.textContent = msg;
  t.hidden = false;
  if (toastTimer) clearTimeout(toastTimer);
  if (ms > 0) toastTimer = setTimeout(() => { t.hidden = true; }, ms);
}

async function api(path, body) {
  const r = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-FSX": "1" },
    body: JSON.stringify(body || {}),
  });
  const j = await r.json().catch(() => ({}));
  if (!r.ok) {
    const e = new Error(j.error || ("HTTP " + r.status));
    e.status = r.status;
    throw e;
  }
  return j;
}
function fail(e) {
  if (e && e.status === 401) { location.reload(); return; }
  toast((e && e.message) || String(e));
}

function render() {
  const self = S.self || {};
  $("#meName").textContent = self.name || "—";
  $("#meID").textContent = self.id ? "ID " + (self.short || shortID(self.id)) : "";
  $("#meID").onclick = () => {
    if (!self.id) return;
    navigator.clipboard.writeText(self.id).then(
      () => toast("已复制完整节点 ID，可发给对方"),
      () => toast("复制失败，请手动选择")
    );
  };
  const rp = $("#relayPill");
  const relayText = { online: "中继 已连接", connecting: "中继 连接中", offline: "中继 离线", off: "中继 未配置" };
  rp.textContent = relayText[S.relay] || ("中继 " + S.relay);
  rp.className = "pill " + (S.relay === "online" ? "ok" : S.relay === "off" ? "" : "off");
  if (S.code) {
    $("#codePill").hidden = false;
    $("#pairCode").textContent = S.code;
  } else {
    $("#codePill").hidden = true;
  }
  renderPeers();
  renderJobs();
  renderInbox();
  renderMsgs();
}

function renderPeers() {
  const box = $("#peers");
  clear(box);
  const peers = S.peers || [];
  $("#peerCount").textContent = peers.length;
  if (!peers.length) {
    box.append(elem("div", "empty", "等待节点上线，或在下方输入节点 ID 添加"));
    return;
  }
  for (const p of peers) {
    const card = elem("div", "peer");
    card.append(elem("span", "dot" + (p.online ? " on" : "")));
    const info = elem("div", "peer-info");
    info.append(elem("div", "peer-name", p.name || p.id || p.addr));
    const sub = p.id ? shortID(p.id) + (p.addr ? " · " + p.addr : " · 中继") : p.addr;
    info.append(elem("div", "peer-sub", sub || ""));
    card.append(info);
    const acts = elem("div", "peer-actions");
    const b1 = elem("button", "btn", "发送");
    b1.onclick = () => pickFiles(p);
    const b2 = elem("button", "btn ghost", "消息");
    b2.onclick = () => askText(p);
    acts.append(b1, b2);
    card.append(acts);
    box.append(card);
  }
}

const LIVE = ["pending", "connecting", "waiting", "active"];

function renderJobs() {
  const box = $("#jobs");
  clear(box);
  const jobs = S.jobs || [];
  const live = jobs.filter((j) => LIVE.includes(j.state));
  const done = jobs.filter((j) => !LIVE.includes(j.state)).slice(0, 6);
  $("#jobCount").textContent = live.length;
  if (!live.length && !done.length) {
    box.append(elem("div", "empty", "暂无传输任务"));
    return;
  }
  for (const j of live) box.append(jobCard(j));
  if (done.length) {
    const sep = elem("div", "panel-head sub");
    sep.append(elem("h2", null, "最近"));
    box.append(sep);
    for (const j of done) box.append(jobCard(j));
  }
}

function jobCard(j) {
  const card = elem("div", "job" + (j.state === "pending" ? " pending" : ""));
  const top = elem("div", "job-top");
  const title = j.state === "pending"
    ? "接收请求 · " + (j.peer_name || shortID(j.peer_id) || "未知节点")
    : (j.dir === "send" ? "发送 · " : "接收 · ") + (j.peer_name || shortID(j.peer_id) || "未知节点");
  top.append(elem("div", "job-title", title));
  if (j.mode) top.append(elem("span", "badge " + (j.mode === "relay" ? "relay" : "ok"), modeLabel(j.mode)));
  top.append(elem("span", "badge" + (j.state === "pending" ? " warn" : ""), stateLabel(j.state)));
  card.append(top);

  const items = j.items || [];
  const names = items.map((i) => i.name);
  const name = names.length === 1 ? names[0]
    : names.length ? names[0] + " 等 " + names.length + " 个文件" : "";
  card.append(elem("div", "job-name", name));

  if (j.state === "pending") {
    const files = elem("div", "files");
    for (const it of items.slice(0, 6)) {
      files.append(elem("span", null, it.name + " · " + fmtSize(it.size)));
    }
    if (items.length > 6) files.append(elem("span", null, "…共 " + items.length + " 个文件"));
    card.append(files);
    if (j.sas) {
      const sas = elem("div", "sas", "两端校验码 ");
      sas.append(elem("b", null, j.sas));
      sas.append(document.createTextNode("，与发送方核对一致再接收"));
      card.append(sas);
    }
    const row = elem("div", "row");
    row.style.marginTop = "10px";
    const lab = elem("label", "sas");
    const cb = document.createElement("input");
    cb.type = "checkbox";
    cb.checked = alwaysAccept;
    cb.onchange = () => { alwaysAccept = cb.checked; };
    lab.append(cb, document.createTextNode(" 总是接收该节点"));
    const ok = elem("button", "btn", "接受");
    ok.onclick = () => decide(j.id, true);
    const no = elem("button", "btn ghost", "拒绝");
    no.onclick = () => decide(j.id, false);
    row.append(lab, ok, no);
    card.append(row);
    return card;
  }

  const pct = j.total > 0 ? Math.min(100, (j.done / j.total) * 100)
    : (j.state === "done" ? 100 : 0);
  const bar = elem("div", "bar");
  const fill = document.createElement("i");
  fill.style.width = pct + "%";
  bar.append(fill);
  card.append(bar);

  const foot = elem("div", "job-foot");
  let left;
  if (j.state === "done") left = "已完成 · " + fmtSize(j.total);
  else if (j.state === "failed") left = "失败：" + (j.error || "未知错误");
  else if (j.state === "rejected") left = "对方拒绝接收";
  else if (j.state === "canceled") left = "已取消";
  else left = fmtSize(j.done) + " / " + fmtSize(j.total) + (j.speed ? " · " + fmtSpeed(j.speed) : "");
  foot.append(elem("span", null, left));

  if (["connecting", "waiting", "active"].includes(j.state)) {
    const c = elem("button", "link", "取消");
    c.onclick = () => cancelJob(j.id);
    foot.append(c);
  } else if (j.state === "done" && j.dir === "recv") {
    const a = document.createElement("a");
    a.className = "link";
    a.href = "/dl/" + j.id;
    a.textContent = items.length > 1 ? "打包下载" : "下载";
    foot.append(a);
  }
  card.append(foot);
  return card;
}

function renderInbox() {
  const box = $("#inbox");
  clear(box);
  const done = (S.jobs || []).filter((j) => j.dir === "recv" && j.state === "done").slice(0, 8);
  if (!done.length) {
    box.append(elem("div", "empty", "还没有收到文件"));
    return;
  }
  for (const j of done) {
    const item = elem("div", "inbox-item");
    const info = elem("div", "peer-info");
    info.append(elem("div", "peer-name",
      (j.items ? j.items.length : 0) + " 个文件 · 来自 " + (j.peer_name || shortID(j.peer_id))));
    const names = (j.items || []).map((i) => i.name);
    info.append(elem("div", "peer-sub",
      names.slice(0, 2).join("、") + (names.length > 2 ? " …" : "")));
    item.append(info);
    const links = elem("div", "row");
    const n = (j.items || []).length;
    if (n === 1) {
      const a = document.createElement("a");
      a.href = "/dl/" + j.id;
      a.textContent = "下载";
      links.append(a);
    } else {
      (j.items || []).slice(0, 3).forEach((it, i) => {
        const a = document.createElement("a");
        a.href = "/dl/" + j.id + "?i=" + i;
        a.textContent = "文件" + (i + 1);
        links.append(a);
      });
      const z = document.createElement("a");
      z.href = "/dl/" + j.id;
      z.textContent = "打包";
      links.append(z);
    }
    item.append(links);
    box.append(item);
  }
}

function renderMsgs() {
  const box = $("#msgs");
  clear(box);
  const msgs = S.messages || [];
  if (!msgs.length) {
    box.append(elem("div", "empty", "暂无消息"));
    return;
  }
  for (const m of msgs.slice(-30)) {
    const el = elem("div", "msg" + (m.dir === "out" ? " out" : ""));
    el.append(elem("div", "who", m.dir === "out" ? "我" : (m.from_name || shortID(m.from_id))));
    el.append(document.createTextNode(m.body));
    box.append(el);
  }
  box.scrollTop = box.scrollHeight;
}

async function decide(id, accept) {
  try {
    await api("/api/decision", { id, accept, always: accept ? alwaysAccept : false });
  } catch (e) { fail(e); }
}
async function cancelJob(id) {
  try { await api("/api/cancel", { id }); } catch (e) { fail(e); }
}

function pickFiles(peer) {
  pendingPeer = peer;
  $("#fileInput").click();
}
$("#fileInput").onchange = (ev) => {
  const files = Array.from(ev.target.files || []);
  ev.target.value = "";
  if (files.length && pendingPeer) upload(pendingPeer, files);
};

function upload(peer, files) {
  if (!peer) return;
  const fd = new FormData();
  if (peer.id) fd.append("peer", peer.id);
  if (peer.addr) fd.append("addr", peer.addr);
  for (const f of files) fd.append("files", f, f.name);

  toast("上传中 0%", 0);
  const xhr = new XMLHttpRequest();
  xhr.open("POST", "/api/upload");
  xhr.setRequestHeader("X-FSX", "1");
  xhr.upload.onprogress = (e) => {
    if (e.lengthComputable) toast("上传中 " + Math.round((e.loaded / e.total) * 100) + "%", 0);
  };
  xhr.onload = () => {
    if (xhr.status === 200) {
      toast("已开始传输");
    } else if (xhr.status === 401) {
      location.reload();
    } else {
      let msg = "上传失败";
      try { msg = JSON.parse(xhr.responseText).error || msg; } catch (_) {}
      toast(msg);
    }
  };
  xhr.onerror = () => toast("上传失败：网络错误");
  xhr.send(fd);
}

function askText(peer) {
  const box = $("#modalBox");
  clear(box);
  box.append(elem("h3", null, "发给 " + (peer.name || peer.addr || shortID(peer.id))));
  const ta = document.createElement("textarea");
  ta.placeholder = "输入要发送的文字…";
  ta.style.cssText = "width:100%;height:110px;resize:vertical;background:#0b0d12;border:1px solid #262e3d;border-radius:9px;color:#e8edf5;padding:10px;font:13px/1.5 inherit;outline:none";
  box.append(ta);
  const row = elem("div", "row");
  row.style.marginTop = "10px";
  row.style.justifyContent = "flex-end";
  const cancel = elem("button", "btn ghost", "取消");
  cancel.onclick = closeModal;
  const send = elem("button", "btn", "发送");
  send.onclick = async () => {
    const body = ta.value.trim();
    if (!body) return;
    try {
      await api("/api/text", { peer: peer.id || "", addr: peer.addr || "", body });
      closeModal();
      toast("已发送");
    } catch (e) { fail(e); }
  };
  row.append(cancel, send);
  box.append(row);
  $("#modal").hidden = false;
  ta.focus();
}

function closeModal() {
  $("#modal").hidden = true;
  clear($("#modalBox"));
}

function choosePeer(files) {
  const online = (S.peers || []).filter((p) => p.online);
  if (!online.length) { toast("没有在线节点"); return; }
  if (online.length === 1) { upload(online[0], files); return; }
  const box = $("#modalBox");
  clear(box);
  box.append(elem("h3", null, "发送到哪个节点？"));
  const list = elem("div", "modal-peers");
  for (const p of online) {
    const b = elem("button", "btn ghost", (p.name || p.addr) + (p.addr ? "  " + p.addr : ""));
    b.style.textAlign = "left";
    b.onclick = () => { closeModal(); upload(p, files); };
    list.append(b);
  }
  box.append(list);
  const row = elem("div", "row");
  row.style.marginTop = "10px";
  row.style.justifyContent = "flex-end";
  const cancel = elem("button", "btn ghost", "取消");
  cancel.onclick = closeModal;
  row.append(cancel);
  box.append(row);
  $("#modal").hidden = false;
}

document.addEventListener("dragenter", (e) => {
  e.preventDefault();
  dragDepth++;
  $("#drop").hidden = false;
});
document.addEventListener("dragover", (e) => e.preventDefault());
document.addEventListener("dragleave", (e) => {
  e.preventDefault();
  dragDepth--;
  if (dragDepth <= 0) { dragDepth = 0; $("#drop").hidden = true; }
});
document.addEventListener("drop", (e) => {
  e.preventDefault();
  dragDepth = 0;
  $("#drop").hidden = true;
  const files = Array.from((e.dataTransfer && e.dataTransfer.files) || []);
  if (files.length) choosePeer(files);
});

$("#addPeer").onclick = async () => {
  const v = $("#manualAddr").value.trim();
  if (!v) return;
  try {
    const body = v.includes(":") ? { addr: v } : { id: v };
    const r = await api("/api/peer", body);
    $("#manualAddr").value = "";
    toast(r && r.name ? "已找到 " + r.name : "已添加");
  } catch (e) { fail(e); }
};
$("#manualAddr").addEventListener("keydown", (e) => {
  if (e.key === "Enter") $("#addPeer").click();
});
$("#modal").addEventListener("click", (e) => {
  if (e.target === $("#modal")) closeModal();
});

function connect() {
  const es = new EventSource("/api/events");
  es.onmessage = (e) => {
    try {
      const next = JSON.parse(e.data);
      next.peers = next.peers || [];
      next.jobs = next.jobs || [];
      next.messages = next.messages || [];
      S = next;
      render();
    } catch (_) {}
  };
  es.onerror = () => {
    es.close();
    setTimeout(connect, 2500);
  };
}

render();
connect();

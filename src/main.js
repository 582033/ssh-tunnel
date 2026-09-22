const invoke = (cmd, args) => window.__TAURI__.core.invoke(cmd, args || {});

const $ = (id) => document.getElementById(id);

const MAX_LOG = 1000;
const logLines = [];

let store = null;
let state = "stopped";
let stateDetail = "";
let dirty = false;

const STATE_TEXT = {
  stopped: "已停止",
  connecting: "连接中…",
  connected: "已连接",
  reconnecting: "重连中…",
  failed: "连接失败",
};

// ---------- 应用内弹窗 / 提示条 ----------
// Tauri 的 webview 不实现 prompt / confirm / alert（调用直接返回 null / false），
// 所有需要用户输入或确认的地方都走这里的弹窗，失败提示走提示条 + 日志。

let modalResolve = null;

function ask(opts) {
  $("modalTitle").textContent = opts.title || "";
  $("modalText").textContent = opts.text || "";
  const input = $("modalInput");
  if (opts.input) {
    input.classList.remove("hidden");
    input.value = opts.defaultValue || "";
    input.placeholder = opts.placeholder || "";
  } else {
    input.classList.add("hidden");
    input.value = "";
  }
  const ok = $("modalOk");
  ok.textContent = opts.okText || "确定";
  ok.className = opts.danger ? "btn danger" : "btn primary";
  $("modal").classList.remove("hidden");
  if (opts.input) setTimeout(() => input.focus(), 30);
  return new Promise((resolve) => { modalResolve = resolve; });
}

function closeModal(value) {
  if (!modalResolve) return;
  const done = modalResolve;
  modalResolve = null;
  $("modal").classList.add("hidden");
  done(value);
}

function modalHasInput() {
  return !$("modalInput").classList.contains("hidden");
}

function showNotice(text) {
  const n = $("notice");
  n.textContent = text;
  n.classList.remove("hidden");
}

function hideNotice() {
  $("notice").classList.add("hidden");
}

// invoke 失败时不能只吞掉：弹窗点完没反应比报错更难排查
function fail(e, what) {
  const msg = typeof e === "string" ? e : `${what}：${JSON.stringify(e)}`;
  appendLogLine(msg);
  showNotice(msg);
}

// ---------- 事件 ----------

window.__TAURI__.event.listen("state", (e) => {
  state = e.payload.state;
  stateDetail = e.payload.detail || "";
  renderBanner();
  renderActions();
});

window.__TAURI__.event.listen("log", (e) => {
  appendLogLine(e.payload.line);
});

function appendLogLine(line) {
  if (line === "") return;
  const t = new Date().toLocaleTimeString("zh-CN", { hour12: false });
  logLines.push(`[${t}] ${line}`);
  while (logLines.length > MAX_LOG) logLines.shift();
  const entry = $("logEntry");
  entry.textContent = logLines.join("\n");
  entry.scrollTop = entry.scrollHeight;
}

// Tauri 里 JS 报错没有任何可见反馈，统一落到日志面板，便于排查
window.addEventListener("error", (e) => {
  appendLogLine("JS 错误: " + (e.message || e));
});
window.addEventListener("unhandledrejection", (e) => {
  const r = e.reason;
  appendLogLine("未处理的异步错误: " + (typeof r === "string" ? r : (r && r.message) || JSON.stringify(r)));
});

// ---------- 渲染 ----------

function renderBanner() {
  const dot = $("dot");
  dot.className = "dot " + state;
  $("pill").className = "pill " + state;
  $("statusText").textContent = STATE_TEXT[state] || state;
  let detail = stateDetail;
  if (state === "failed" && detail.length > 70) {
    detail = detail.slice(0, 70) + "…";
  }
  if (state === "stopped" && !detail) {
    detail = "代理未启动";
  }
  $("proxyText").textContent = detail || "—";
}

function renderActions() {
  const running = state === "connecting" || state === "connected" || state === "reconnecting";
  $("btnToggle").textContent = running ? "断开" : "连接";
  $("btnToggle").className = running ? "btn grow accent" : "btn grow primary";
  $("btnChrome").disabled = !(state === "connected");
  $("btnCopy").disabled = !(state === "connected");
}

function renderPicker() {
  const sel = $("picker");
  sel.innerHTML = "";
  for (const name of store.names) {
    const opt = document.createElement("option");
    opt.value = name;
    opt.textContent = name;
    if (name === store.active) opt.selected = true;
    sel.appendChild(opt);
  }
}

function renderStatusTab() {
  const c = store.current;
  const rows = [
    ["服务器", c.serverAddr ? `${c.serverAddr}:${c.serverPort}` : "—"],
    ["用户名", c.username || "—"],
    ["认证", c.privateKey ? "私钥文件" : c.password ? "密码" : "—"],
    ["本地代理", `socks5://127.0.0.1:${c.localPort}`],
    ["域名解析", c.customDNS ? `${c.customDNS}（经隧道查询）` : "由 SSH 服务器解析（推荐）"],
    ["Chrome", c.useChrome ? "连接后自动打开无痕窗口" : "不自动打开"],
  ];
  const grid = $("infoGrid");
  grid.innerHTML = "";
  for (const [k, v] of rows) {
    const key = document.createElement("div");
    key.className = "key";
    key.textContent = k;
    const val = document.createElement("div");
    val.className = "value";
    val.textContent = v;
    grid.appendChild(key);
    grid.appendChild(val);
  }
  const cp = $("confPath");
  cp.textContent = store.path || "—";
}

function fillForm() {
  const c = store.current;
  $("f-name").value = c.name || "";
  $("f-serverAddr").value = c.serverAddr || "";
  $("f-serverPort").value = c.serverPort || "";
  $("f-username").value = c.username || "";
  $("f-password").value = c.password || "";
  $("f-privateKey").value = c.privateKey || "";
  $("f-localPort").value = c.localPort || "";
  const dnsMode = c.customDNS ? "custom" : "remote";
  document.querySelector(`input[name="dnsMode"][value="${dnsMode}"]`).checked = true;
  $("f-customDNS").value = c.customDNS || "";
  $("f-customDNS-wrap").style.opacity = dnsMode === "custom" ? "1" : ".4";
  $("f-chromePath").value = c.chromePath || "";
  $("f-useChrome").checked = !!c.useChrome;
  $("f-autoConn").checked = !!c.autoConnect;
  dirty = false;
}

function collectForm() {
  const dnsCustom = document.querySelector('input[name="dnsMode"]:checked').value === "custom";
  return {
    name: $("f-name").value.trim(),
    serverAddr: $("f-serverAddr").value.trim(),
    serverPort: $("f-serverPort").value.trim(),
    username: $("f-username").value.trim(),
    password: $("f-password").value,
    privateKey: $("f-privateKey").value.trim(),
    localPort: $("f-localPort").value.trim(),
    customDNS: dnsCustom ? $("f-customDNS").value.trim() : "",
    chromePath: $("f-chromePath").value.trim(),
    useChrome: $("f-useChrome").checked,
    autoConnect: $("f-autoConn").checked,
  };
}

function markDirty() { dirty = true; }

function bindFormEvents() {
  for (const el of document.querySelectorAll("#settingsForm input")) {
    el.addEventListener("input", markDirty);
    el.addEventListener("change", markDirty);
  }
}

async function saveConfig(cfg) {
  await invoke("save_config", { cfg });
  const s = await refreshStore();
  appendLogLine("已保存连接「" + s.current.name + "」到 " + s.path);
  stateDetail = s.current.serverAddr
    ? `配置已保存（${s.path}）`
    : "代理未启动";
  renderStatusTab();
}

async function refreshStore() {
  store = await invoke("get_store");
  renderPicker();
  renderStatusTab();
  fillForm();
  return store;
}

// ---------- 交互 ----------

async function onToggle() {
  if (isRunning()) {
    try {
      await invoke("stop");
    } catch (e) {
      fail(e, "停止失败");
    }
  } else {
    try {
      await invoke("start");
    } catch (e) {
      const msg = typeof e === "string" ? e : JSON.stringify(e);
      appendLogLine("无法连接: " + msg);
      switchTab("settings");
    }
  }
}

async function onOpenChrome() {
  await invoke("start_browser");
}

async function onCopy() {
  const c = store.current;
  const addr = `socks5://127.0.0.1:${c.localPort}`;
  try {
    await navigator.clipboard.writeText(addr);
    appendLogLine(`已复制代理地址：${addr}`);
  } catch (e) {
    appendLogLine("复制失败：" + e);
    showNotice("复制失败：" + e);
  }
}

async function onPickerChange() {
  const name = $("picker").value;
  if (!name || name === store.active) return;
  if (dirty) {
    const ok = await ask({
      title: "切换连接",
      text: "设置页有未保存的修改，切换连接将丢弃它们。",
      okText: "继续",
      danger: true,
    });
    if (!ok) {
      $("picker").value = store.active;
      return;
    }
  }
  const wasRunning = isRunning();
  try {
    if (needsStop()) await invoke("stop");
    await invoke("set_active", { name });
    store = await invoke("get_store");
    renderPicker();
    fillForm();
    renderStatusTab();
    if (wasRunning) {
      await invoke("start");
    }
  } catch (e) {
    fail(e, "切换连接失败");
  }
}

async function onAdd() {
  const name = await ask({
    title: "新建连接",
    text: "给新连接起个名字，本地端口会自动顺延。",
    input: true,
    defaultValue: "新连接",
    placeholder: "连接名称",
    okText: "创建",
  });
  appendLogLine("新建连接: " + (name || "(已取消)"));
  if (!name) return;
  // 新建的连接会成为当前连接，而运行中不允许换配置，所以先停
  const wasRunning = isRunning();
  try {
    if (wasRunning) {
      await invoke("stop");
      appendLogLine("已停止隧道，以切换到新建的连接");
    }
    await invoke("add_connection", { name });
    await refreshStore();
    appendLogLine("已新建连接「" + name + "」并切到该连接");
    appendLogLine("填好服务器信息后点「保存并连接」");
    switchTab("settings");
  } catch (e) {
    fail(e, "新建连接失败");
  }
}

async function onDelete() {
  const name = store.active;
  const ok = await ask({
    title: "删除连接",
    text: `确定删除连接「${name}」？此操作不可撤销。`,
    okText: "删除",
    danger: true,
  });
  appendLogLine("删除连接: " + name + (ok ? "" : "（已取消）"));
  if (!ok) return;
  try {
    await invoke("delete_connection", { name });
    await refreshStore();
    appendLogLine("已删除连接「" + name + "」");
  } catch (e) {
    fail(e, "删除连接失败");
  }
}

function isRunning() {
  return state === "connecting" || state === "connected" || state === "reconnecting";
}

// 「连接失败」时后端仍在退避重连，改配置/切连接前一样要先停掉
function needsStop() {
  return isRunning() || state === "failed";
}

async function onSave(andConnect) {
  const wasRunning = isRunning();
  if (needsStop()) await invoke("stop");
  let cfg;
  try {
    cfg = collectForm();
    await saveConfig(cfg);
    hideNotice();
  } catch (e) {
    const msg = typeof e === "string" ? e : "保存失败：" + JSON.stringify(e);
    appendLogLine(msg);
    showNotice(msg);
    return;
  }
  if (andConnect || wasRunning) {
    await invoke("start");
  }
}

function switchTab(tab) {
  for (const b of document.querySelectorAll(".tab")) {
    b.classList.toggle("active", b.dataset.tab === tab);
  }
  for (const p of document.querySelectorAll(".panel")) {
    p.classList.toggle("active", p.id === "panel-" + tab);
  }
}

async function boot() {
  const res = await invoke("get_store");
  store = res;
  if (res.load_error) {
    appendLogLine("配置需要完善: " + res.load_error);
    showNotice("配置需要完善：" + res.load_error + "（请在「设置」中填写后点「保存并连接」）");
    switchTab("settings");
  } else if (store.current.autoConnect) {
    setTimeout(() => invoke("start").catch((e) => appendLogLine("启动失败: " + e)), 0);
  }

  renderPicker();
  renderStatusTab();
  renderBanner();
  renderActions();
  fillForm();
  bindFormEvents();
}

$("btnToggle").addEventListener("click", onToggle);
$("btnChrome").addEventListener("click", onOpenChrome);
$("btnCopy").addEventListener("click", onCopy);
$("picker").addEventListener("change", onPickerChange);
$("btnAdd").addEventListener("click", onAdd);
$("btnDel").addEventListener("click", onDelete);
$("btnRevert").addEventListener("click", () => { fillForm(); });
$("btnSave").addEventListener("click", () => onSave(false));
$("btnSaveConnect").addEventListener("click", () => onSave(true));
$("btnCopyLog").addEventListener("click", () => {
  navigator.clipboard.writeText(logLines.join("\n")).then(
    () => appendLogLine("已复制全部日志"),
    () => appendLogLine("复制失败"));
});
$("btnClearLog").addEventListener("click", () => {
  logLines.length = 0;
  $("logEntry").textContent = "";
});
$("modalOk").addEventListener("click", () => {
  closeModal(modalHasInput() ? $("modalInput").value.trim() || null : true);
});
$("modalCancel").addEventListener("click", () => closeModal(null));
$("modalInput").addEventListener("keydown", (e) => {
  if (e.key === "Enter") $("modalOk").click();
  if (e.key === "Escape") closeModal(null);
});
for (const b of document.querySelectorAll(".tab")) {
  b.addEventListener("click", () => switchTab(b.dataset.tab));
}

boot();
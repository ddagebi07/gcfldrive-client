"use strict";
// 극플드라이브 화면. PIN은 이 페이지가 아니라 클라이언트 프로그램(Go)이 들고 있다.

const $ = (id) => document.getElementById(id);
const ICON = {
  clock: '<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/><path d="M12 6v6l4 2"/></svg>',
  print: '<svg viewBox="0 0 24 24"><path d="M6 9V3h12v6M6 18H4a2 2 0 0 1-2-2v-5a2 2 0 0 1 2-2h16a2 2 0 0 1 2 2v5a2 2 0 0 1-2 2h-2"/><rect x="6" y="14" width="12" height="7"/></svg>',
  clear: '<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/><path d="M15 9l-6 6M9 9l6 6"/></svg>',
  eyeOff: '<svg viewBox="0 0 24 24"><path d="M17.9 17.9A10 10 0 0 1 12 20C5 20 1 12 1 12a18 18 0 0 1 5-5.9M9.9 4.2A9 9 0 0 1 12 4c7 0 11 8 11 8a18 18 0 0 1-2.2 3.2M14.1 14.1a3 3 0 1 1-4.2-4.2M1 1l22 22"/></svg>',
  eye: '<svg viewBox="0 0 24 24"><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8S1 12 1 12z"/><circle cx="12" cy="12" r="3"/></svg>',
};

let maxSheets = 7;
let files = [];
let fetchedAt = 0;
let busyPrinting = false;

async function call(path, body) {
  const res = await fetch(path, body === undefined ? {} : {
    method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body),
  });
  const text = await res.text();
  let data = {};
  try { data = text ? JSON.parse(text) : {}; } catch (_) { /* not JSON */ }
  if (!res.ok) throw Object.assign(new Error(data.error || "문제가 생겼어요. 다시 시도해 주세요."), { status: res.status });
  return data;
}

/* ---------- 대화상자 ---------- */
function dialog({ title, body = "", extra = null, buttons, tone = "" }) {
  return new Promise((resolve) => {
    const scrim = $("dialogScrim");
    scrim.querySelector(".dialog").dataset.tone = tone;
    $("dialogTitle").textContent = title;
    $("dialogBody").textContent = body;
    const extraBox = $("dialogExtra");
    extraBox.replaceChildren(...(extra ? [extra] : []));
    const actions = $("dialogActions");
    actions.replaceChildren();
    const done = (value) => { scrim.hidden = true; resolve(value); };
    buttons.forEach((b) => {
      const el = document.createElement("button");
      el.type = "button";
      el.className = "btn " + (b.kind || "outline");
      el.textContent = b.label;
      el.onclick = () => done(b.value);
      actions.appendChild(el);
    });
    $("dialogClose").onclick = () => done(null);
    scrim.hidden = false;
    (actions.querySelector(".btn:last-child") || $("dialogClose")).focus();
  });
}
const notice = (title, body) => dialog({ title, body, buttons: [{ label: "확인", kind: "primary", value: true }] });
function setBusy(text) { $("busyText").textContent = text || ""; $("busy").hidden = !text; }

/* ---------- 화면 전환 ---------- */
function showLogin() {
  clearPrint();
  files = [];
  $("drive").hidden = true;
  $("login").hidden = false;
  $("dialogScrim").hidden = true;
  $("hakbun").value = "";
  $("pin").value = "";
  setHakbunError("");
  setPinError("");
  $("hakbun").focus();
}
const HELLOS = [
  ["en", "Welcome!"], ["ja", "ようこそ！"], ["zh", "欢迎光临！"], ["fr", "Bienvenue !"], ["de", "Willkommen!"],
];
let lastHello = -1;
function pickHello() {
  let i;
  do { i = Math.floor(Math.random() * HELLOS.length); } while (i === lastHello);
  lastHello = i;
  const [lang, text] = HELLOS[i];
  $("hello").lang = lang;
  $("hello").textContent = text;
}
function showDrive(hakbun, list) {
  pickHello();
  $("who").textContent = hakbun + "님";
  setFiles(list);
  $("login").hidden = true;
  $("drive").hidden = false;
}

/* ---------- 로그인 ---------- */
function setFieldError(field, errorEl, msg) {
  $(field).classList.toggle("invalid", !!msg);
  $(errorEl).hidden = !msg;
  $(errorEl).querySelector("span").textContent = msg;
}
const setPinError = (msg) => setFieldError("pinField", "pinError", msg);
const setHakbunError = (msg) => setFieldError("hakbunField", "hakbunError", msg);
$("hakbun").addEventListener("input", (e) => { e.target.value = e.target.value.replace(/\D/g, "").slice(0, 5); setHakbunError(""); });
$("pin").addEventListener("input", (e) => { e.target.value = e.target.value.replace(/\D/g, "").slice(0, 4); setPinError(""); });
$("eye").addEventListener("click", () => {
  const input = $("pin");
  const hide = input.type === "password";
  input.type = hide ? "text" : "password";
  $("eye").innerHTML = hide ? ICON.eyeOff : ICON.eye;
  $("eye").setAttribute("aria-label", hide ? "PIN 숨기기" : "PIN 보기");
  input.focus();
});
$("loginForm").addEventListener("submit", async (e) => {
  e.preventDefault();
  const hakbun = $("hakbun").value;
  const pin = $("pin").value;
  let bad = false;
  if (!hakbun) { setHakbunError("학번을 입력해 주세요."); bad = true; }
  else if (!/^\d{5}$/.test(hakbun)) { setHakbunError("학번 5자리를 입력해 주세요."); bad = true; }
  if (!pin) { setPinError("PIN을 입력해 주세요."); bad = true; }
  else if (!/^\d{4}$/.test(pin)) { setPinError("PIN 4자리를 입력해 주세요."); bad = true; }
  if (bad) return;
  $("loginBtn").disabled = true;
  try {
    const res = await call("/api/login", { hakbun, pin });
    showDrive(res.hakbun, res.files);
  } catch (err) {
    if (/학번/.test(err.message) && err.status === 400) setHakbunError(err.message);
    else setPinError(err.message);
  } finally {
    $("loginBtn").disabled = false;
  }
});

$("otherWay").addEventListener("click", () => call("/api/other-way", {}).catch(() => {}));
$("logout").addEventListener("click", () => call("/api/logout", {}).then(showLogin, showLogin));

/* ---------- 파일 목록 ---------- */
function remainingText(ms) {
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s >= 86400) return Math.floor(s / 86400) + "d";
  if (s >= 3600) return Math.floor(s / 3600) + "h";
  if (s >= 60) return Math.floor(s / 60) + "m " + (s % 60) + "s";
  return s + "s";
}
function setFiles(list) { files = list || []; fetchedAt = Date.now(); renderFiles(); }
function renderFiles() {
  const panel = $("panel");
  panel.classList.toggle("empty", files.length === 0);
  if (files.length === 0) {
    panel.innerHTML = '<div class="empty-row"><div class="qr"><img src="img/qr.svg" alt=""></div>' +
      "<div><strong>보관 중인 파일이 없습니다.</strong><span>gcflarchive.co.kr/share</span></div></div>";
    return;
  }
  const rows = files.map((f) => {
    const row = document.createElement("div");
    row.className = "row";
    row.dataset.id = f.id;
    row.innerHTML = '<div class="badge"></div><div class="card"><span class="name"></span>' +
      `<button type="button" class="link print">${ICON.print}출력</button>` +
      `<button type="button" class="link clear">${ICON.clear}지우기</button></div>`;
    row.querySelector(".name").textContent = f.name;
    row.querySelector(".name").title = f.name;
    row.querySelector(".print").onclick = () => printFile(f);
    row.querySelector(".clear").onclick = () => clearFile(f);
    return row;
  });
  panel.replaceChildren(...rows);
  tickBadges();
}
function tickBadges() {
  const elapsed = Date.now() - fetchedAt;
  let expired = false;
  document.querySelectorAll("#panel .row").forEach((row) => {
    const f = files.find((x) => x.id === row.dataset.id);
    if (!f) return;
    const left = f.remainingMs - elapsed;
    if (left <= 0) expired = true;
    const badge = row.querySelector(".badge");
    badge.classList.toggle("urgent", f.urgent);
    badge.innerHTML = ICON.clock + "<span>" + remainingText(left) + "</span>";
  });
  if (expired && !busyPrinting) refreshFiles();
}
async function refreshFiles() {
  try { setFiles((await call("/api/files")).files); } catch (err) { if (err.status === 401) showLogin(); }
}
async function clearFile(f) {
  const ok = await dialog({
    title: "파일을 삭제하시겠어요?", body: "삭제된 파일은 복구할 수 없습니다.",
    buttons: [{ label: "취소", kind: "outline", value: false }, { label: "삭제", kind: "danger", value: true }],
  });
  if (!ok) return;
  try { await call("/api/delete", { id: f.id }); } catch (err) { await notice("삭제하지 못했어요", err.message); }
  refreshFiles();
}

/* ---------- 출력 ---------- */
const printArea = $("printArea");
let urls = [];
function clearPrint() {
  printArea.replaceChildren();
  $("hwpHost").replaceChildren();
  urls.forEach((u) => URL.revokeObjectURL(u));
  urls = [];
  busyPrinting = false;
  setBusy("");
}
function keep(blob) { const u = URL.createObjectURL(blob); urls.push(u); return u; }

function addPage(img, w, h) {
  const pg = document.createElement("div");
  pg.className = "pg" + (w > h ? " land" : "");
  pg.appendChild(img);
  printArea.appendChild(pg);
}
function loadImage(src) {
  return new Promise((resolve, reject) => {
    const img = new Image();
    img.onload = () => resolve(img);
    img.onerror = () => reject(new Error("그림을 불러오지 못했어요."));
    img.src = src;
  });
}

async function pagesFromPdf(buffer) {
  pdfjsLib.GlobalWorkerOptions.workerSrc = "vendor/pdf.worker.min.js";
  const pdf = await pdfjsLib.getDocument({ data: buffer }).promise;
  if (pdf.numPages > maxSheets * 2) return pdf.numPages; // 너무 많아서 그리지 않는다
  const canvas = document.createElement("canvas");
  for (let n = 1; n <= pdf.numPages; n++) {
    const page = await pdf.getPage(n);
    const base = page.getViewport({ scale: 1 });
    const scale = (base.width > base.height ? 1754 : 1240) / base.width; // A4 150dpi
    const vp = page.getViewport({ scale });
    canvas.width = Math.round(vp.width);
    canvas.height = Math.round(vp.height);
    const ctx = canvas.getContext("2d");
    ctx.fillStyle = "#fff";
    ctx.fillRect(0, 0, canvas.width, canvas.height);
    await page.render({ canvasContext: ctx, viewport: vp }).promise;
    let blob = await new Promise((r) => canvas.toBlob(r, "image/png"));
    if (blob.size > 1.5 * 1024 * 1024) blob = await new Promise((r) => canvas.toBlob(r, "image/jpeg", 0.92));
    addPage(await loadImage(keep(blob)), base.width, base.height);
    page.cleanup();
  }
  canvas.width = canvas.height = 0;
  return pdf.numPages;
}

async function pagesFromHwp(bytes, name) {
  const { createEditor } = await import("/static/js/rhwp/index.js");
  const editor = await createEditor("#hwpHost", { studioUrl: "/static/editor/index.html?chrome=embed", handshakeTimeoutMs: 30000 });
  try {
    await editor.loadFile(bytes, name.replace(/ \[.*\]$/, "") + ".hwp", { suppressDialogs: true, skipUnsavedGuard: true });
    const count = await editor.pageCount();
    if (count > maxSheets * 2) return count;
    for (let i = 0; i < count; i++) {
      const svg = await editor.getPageSvg(i);
      const img = await loadImage(keep(new Blob([svg], { type: "image/svg+xml" }))); // <img>로만 그려서 문서 안의 스크립트는 돌지 않는다
      addPage(img, img.naturalWidth, img.naturalHeight);
    }
    return count;
  } finally {
    try { editor.destroy(); } catch (_) { /* already gone */ }
    $("hwpHost").replaceChildren();
  }
}

async function pagesFromText(buffer) {
  let text;
  try { text = new TextDecoder("utf-8", { fatal: true }).decode(buffer); } catch (_) { text = new TextDecoder("euc-kr").decode(buffer); }
  const box = document.createElement("div");
  box.className = "pg-text";
  box.textContent = text;
  printArea.appendChild(box);
  box.style.position = "fixed"; // 쪽 수를 재려고 잠깐 화면 밖에서 펼쳐 본다
  box.style.visibility = "hidden";
  const usable = (297 - 36) * 3.7795; // mm → px
  const pages = Math.max(1, Math.ceil((box.getBoundingClientRect().height - 2 * 18 * 3.7795) / usable));
  box.style.position = box.style.visibility = "";
  return pages;
}

async function printFile(f) {
  if (busyPrinting) return;
  busyPrinting = true;
  clearPrintArea();
  setBusy("파일을 준비하고 있어요");
  let blobId = null;
  try {
    const got = await call("/api/print", { id: f.id, name: f.name });
    blobId = got.blob;
    const res = await fetch("/api/blob/" + blobId);
    const buffer = await res.arrayBuffer();
    let pages;
    if (got.kind === "pdf") pages = await pagesFromPdf(buffer);
    else if (got.kind === "hwp") pages = await pagesFromHwp(new Uint8Array(buffer), f.name);
    else if (got.kind === "image") {
      const img = await loadImage(keep(new Blob([buffer], { type: res.headers.get("Content-Type") || "image/png" })));
      addPage(img, img.naturalWidth, img.naturalHeight);
      pages = 1;
    } else pages = await pagesFromText(buffer);
    call("/api/blob/" + blobId + "/drop", {}).catch(() => {});
    blobId = null;
    setBusy("");

    // 학교 방침: 한 번에 종이 maxSheets장까지. (양면이면 쪽수의 절반이 장수다.)
    if (pages > maxSheets * 2) {
      clearPrint();
      await notice("한 번에 출력할 수 없어요", `이 파일은 ${pages}쪽이에요.\n학교 방침상 한 번에 종이 ${maxSheets}장까지만 출력할 수 있어요.`);
      return;
    }
    if (pages > maxSheets) {
      const go = await dialog({
        title: `${maxSheets}장을 넘으면 출력이 취소돼요`,
        body: `이 파일은 ${pages}쪽이에요.\n양면 인쇄로 종이 ${maxSheets}장 이하가 될 때만 출력돼요. 한 면씩 인쇄하면 취소됩니다.`,
        buttons: [{ label: "취소", kind: "outline", value: false }, { label: "계속", kind: "primary", value: true }],
      });
      if (!go) { clearPrint(); return; }
    }
    await printNow();
  } catch (err) {
    clearPrint();
    await notice("출력하지 못했어요", err.message || "다시 시도해 주세요.");
  } finally {
    if (blobId) call("/api/blob/" + blobId + "/drop", {}).catch(() => {});
    busyPrinting = false;
    refreshFiles();
  }
}
function clearPrintArea() { printArea.replaceChildren(); urls.forEach((u) => URL.revokeObjectURL(u)); urls = []; }

function printNow() {
  return new Promise((resolve) => {
    let finished = false;
    const keepAlive = setInterval(() => call("/api/activity", {}).catch(() => {}), 10000); // 인쇄 창이 열려 있는 동안은 로그아웃하지 않는다
    const finish = () => {
      if (finished) return;
      finished = true;
      clearInterval(keepAlive);
      window.removeEventListener("afterprint", finish);
      clearPrint();
      resolve();
    };
    window.addEventListener("afterprint", finish);
    setTimeout(finish, 3 * 60 * 1000);
    window.print();
  });
}

/* ---------- 상태·자동 로그아웃 ---------- */
let lastActivity = 0;
function noteActivity() {
  const now = Date.now();
  if (now - lastActivity < 2000) return;
  lastActivity = now;
  call("/api/activity", {}).catch(() => {});
}
["pointerdown", "pointermove", "keydown", "wheel", "touchstart"].forEach((t) => window.addEventListener(t, noteActivity, { passive: true }));

function formatRemain(ms) {
  const s = Math.max(0, Math.ceil(ms / 1000));
  return Math.floor(s / 60) + "분 " + String(s % 60).padStart(2, "0") + "초";
}
async function pollState() {
  try {
    const st = await call("/api/state");
    maxSheets = st.maxSheets || maxSheets;
    document.body.classList.toggle("full", !!st.fullscreen);
    if (!st.loggedIn && !$("drive").hidden) showLogin();
    if (st.loggedIn) {
      $("remain").textContent = formatRemain(st.remainingMs);
      $("remain").parentElement.classList.toggle("soon", st.remainingMs < 30000);
    }
  } catch (_) { /* 잠깐 끊겨도 다음 번에 다시 */ }
}
setInterval(pollState, 1000);
setInterval(() => { if (!$("drive").hidden) tickBadges(); }, 1000);
setInterval(() => { if (!$("drive").hidden && !busyPrinting) refreshFiles(); }, 30000);
pollState();

/* ---------- 관리자 ---------- */
let logoClicks = [];
$("logo").addEventListener("click", () => {
  const now = Date.now();
  logoClicks = logoClicks.filter((t) => now - t < 4000).concat(now);
  if (logoClicks.length >= 7) { logoClicks = []; openAdmin(); }
});
window.addEventListener("keydown", (e) => { if (e.ctrlKey && e.shiftKey && e.key === "F12") openAdmin(); });

let adminOpen = false;
async function openAdmin() {
  if (adminOpen) return;
  adminOpen = true;
  try { await adminFlow(); } finally { adminOpen = false; }
}
window.openAdmin = openAdmin;

async function adminFlow() {
  const input = document.createElement("input");
  input.type = "password";
  input.placeholder = "관리자 비밀번호";
  input.autocomplete = "off";
  const asked = dialog({
    title: "관리자 확인", body: "프로그램을 종료하거나 제거하려면 관리자 비밀번호가 필요해요.", extra: input,
    buttons: [{ label: "취소", kind: "outline", value: false }, { label: "확인", kind: "primary", value: true }],
  });
  input.focus();
  input.addEventListener("keydown", (e) => { if (e.key === "Enter") $("dialogActions").querySelector(".primary").click(); });
  if (!(await asked)) return;
  const password = input.value;
  try { await call("/api/admin/check", { password }); } catch (err) { return notice("확인하지 못했어요", err.message); }

  const stack = document.createElement("div");
  stack.className = "stack";
  const choice = dialog({ title: "관리자 메뉴", body: "종료하면 10분 동안 보호가 풀리고, 10분 뒤에 자동으로 다시 실행돼요.", extra: stack, buttons: [{ label: "닫기", kind: "outline", value: null }] });
  [["프로그램 종료 (10분간 보호 해제)", "pause", "primary"], ["프로그램 제거", "remove", "danger"]].forEach(([label, value, kind]) => {
    const b = document.createElement("button");
    b.type = "button"; b.className = "btn " + kind; b.textContent = label;
    b.onclick = () => { $("dialogScrim").hidden = true; stack.dispatchEvent(new CustomEvent("pick", { detail: value })); };
    stack.appendChild(b);
  });
  const picked = await Promise.race([choice, new Promise((r) => stack.addEventListener("pick", (e) => r(e.detail), { once: true }))]);
  try {
    if (picked === "pause") { await call("/api/admin/maintenance", { password, minutes: 10 }); setBusy("종료하고 있어요"); }
    if (picked === "remove") {
      const sure = await dialog({ title: "프로그램을 제거할까요?", body: "이 PC에서 극플드라이브와 보호 서비스가 사라져요.", buttons: [{ label: "취소", kind: "outline", value: false }, { label: "제거", kind: "danger", value: true }] });
      if (sure) { await call("/api/admin/uninstall", { password }); setBusy("제거하고 있어요"); }
    }
  } catch (err) { await notice("처리하지 못했어요", err.message); }
}

document.addEventListener("contextmenu", (e) => e.preventDefault());

/* ---------- 출력 제한 경고 (보호 서비스가 취소한 출력) ---------- */
window.showPrintNotice = async ({ document: doc, sheets, max }) => {
  const box = document.createElement("div");
  box.className = "notice-box";
  const name = document.createElement("p");
  name.className = "notice-doc";
  name.textContent = doc || "문서";
  box.innerHTML =
    '<div class="notice-stats">' +
    `<div class="over"><span>이번 출력</span><b>약 ${sheets}장</b></div>` +
    `<div><span>한 번에 최대</span><b>${max}장</b></div></div>` +
    '<p class="notice-tip">양면 인쇄를 하거나, 나눠서 출력해 주세요.</p>';
  box.prepend(name);
  await dialog({
    title: `한 번에 ${max}장까지만\n출력할 수 있어요`,
    body: "학교 방침에 따라 출력을 취소했어요.",
    extra: box,
    buttons: [{ label: "확인", kind: "primary", value: true }],
    tone: "warning",
  });
  call("/api/notice-done", {}).catch(() => {});
};

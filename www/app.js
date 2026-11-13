'use strict';

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => Array.from(document.querySelectorAll(sel));

const state = {
  config: { asr: false, llm: false },
  settings: null,
  providers: { llm: [], asr: [] },
  recordFormat: 'wav',
  downloadFormat: 'wav',
  q: '', view: 'all', folder: '', tag: '', sort: 'updated',
  items: [],
  stats: { total: 0, today: 0, starred: 0, due: 0, folders: [], tags: [] },
  selectedId: null, current: null,
  rec: null, replaceTarget: null,
  notified: new Set(),
  saveTimer: null, searchTimer: null, saving: false,
};

/* ---------- 基础工具 ---------- */
function esc(value) {
  return String(value == null ? '' : value)
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}

function highlight(text, query) {
  const safe = esc(text || '');
  const tokens = String(query || '').trim().split(/\s+/).filter(Boolean).slice(0, 8);
  if (!tokens.length) return safe;
  const pattern = tokens.map((t) => esc(t).replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('|');
  if (!pattern) return safe;
  try { return safe.replace(new RegExp(`(${pattern})`, 'gi'), '<mark>$1</mark>'); }
  catch (_) { return safe; }
}

const pad = (n) => String(n).padStart(2, '0');

function fmtDuration(ms) {
  const total = Math.max(0, Math.round((Number(ms) || 0) / 1000));
  return `${pad(Math.floor(total / 60))}:${pad(total % 60)}`;
}

function fmtDate(iso) {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  const now = new Date();
  if (d.toDateString() === now.toDateString()) return `今天 ${pad(d.getHours())}:${pad(d.getMinutes())}`;
  if (d.getFullYear() === now.getFullYear()) return `${d.getMonth() + 1}月${d.getDate()}日 ${pad(d.getHours())}:${pad(d.getMinutes())}`;
  return `${d.getFullYear()}/${d.getMonth() + 1}/${d.getDate()}`;
}

function fmtDateTime(iso) {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function fmtBytes(n) {
  const b = Number(n) || 0;
  if (b < 1024) return `${b} B`;
  if (b < 1024 * 1024) return `${(b / 1024).toFixed(1)} KB`;
  return `${(b / 1024 / 1024).toFixed(1)} MB`;
}

function toLocalInput(iso) {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function parseTags(text) {
  const seen = new Set();
  const out = [];
  for (const raw of String(text || '').split(/[,，、]/)) {
    const tag = raw.trim().slice(0, 30);
    if (tag && !seen.has(tag)) { seen.add(tag); out.push(tag); }
    if (out.length >= 20) break;
  }
  return out;
}

function statusInfo(status) {
  const map = {
    none: ['未转写', 'none'], pending: ['处理中', 'pending'], done: ['已转写', 'done'],
    manual: ['手动录入', 'manual'], failed: ['失败', 'failed'],
  };
  return map[status] || map.none;
}

function badgeInfo(status) {
  const map = {
    none: ['未处理', 'none'], pending: ['处理中', 'pending'], done: ['已完成', 'done'],
    manual: ['手动录入', 'manual'], failed: ['失败', 'failed'],
  };
  return map[status] || map.none;
}

function setBadge(el, status) {
  const [text, cls] = badgeInfo(status);
  el.textContent = text;
  el.className = `badge ${cls}`;
}

async function api(path, opts = {}) {
  let res;
  try { res = await fetch(path, opts); }
  catch (_) { throw new Error('无法连接套件服务，请确认应用已在应用中心启动'); }
  const type = res.headers.get('content-type') || '';
  const data = type.includes('application/json') ? await res.json().catch(() => null) : null;
  if (!res.ok) {
    const err = new Error((data && data.error) || `请求失败 (${res.status})`);
    err.status = res.status;
    err.code = data && data.code;
    err.data = data;
    throw err;
  }
  return data;
}

function toast(message, type = '') {
  const el = document.createElement('div');
  el.className = `toast ${type}`;
  el.textContent = message;
  $('#toast-host').appendChild(el);
  setTimeout(() => {
    el.style.transition = 'opacity .25s';
    el.style.opacity = '0';
    setTimeout(() => el.remove(), 260);
  }, 3200);
}

/* ---------- 配置与列表渲染 ---------- */
async function loadConfig() {
  const cfg = await api('/api/config');
  state.config = cfg;
  $('#cap-asr').classList.toggle('on', Boolean(cfg.asr));
  $('#cap-asr-text').textContent = cfg.asr ? cfg.asrModel : '手动录入';
  $('#cap-llm').classList.toggle('on', Boolean(cfg.llm));
  $('#cap-llm-text').textContent = cfg.llm ? cfg.llmModel : '手动填写';
  $('#data-dir').textContent = `数据：${cfg.dataDir}`;
  if (cfg.warning) toast(cfg.warning, 'warn');
  try {
    const s = await api('/api/settings');
    state.settings = s;
    state.providers = s.providers || { llm: [], asr: [] };
    state.recordFormat = (s.record && s.record.format) || cfg.recordFormat || 'wav';
    state.downloadFormat = (s.record && s.record.downloadFormat) || cfg.downloadFormat || 'wav';
  } catch (_) {
    state.recordFormat = cfg.recordFormat || 'wav';
    state.downloadFormat = cfg.downloadFormat || 'wav';
  }
}

async function refresh() {
  const params = new URLSearchParams();
  if (state.q) params.set('q', state.q);
  if (state.view !== 'all') params.set('view', state.view);
  if (state.folder) params.set('folder', state.folder);
  if (state.tag) params.set('tag', state.tag);
  params.set('sort', state.sort);
  try {
    const data = await api(`/api/memos?${params.toString()}`);
    state.items = data.items;
    state.stats = data.stats;
    renderAll();
  } catch (err) {
    toast(err.message, 'error');
  }
}

function renderAll() {
  renderSidebar();
  renderCounts();
  renderContext();
  renderList();
  renderDetailOptions();
}

function renderSidebar() {
  $$('#nav-views button').forEach((b) => b.classList.toggle('active', b.dataset.view === state.view));
  const folders = state.stats.folders || [];
  $('#folder-list').innerHTML = folders.length
    ? folders.map((f) => `<button type="button" class="chip ${state.folder === f.name ? 'active' : ''}" data-folder="${esc(f.name)}"><span class="label">${esc(f.name)}</span><span class="num">${f.count}</span></button>`).join('')
    : '<div class="chip-empty">暂无文件夹</div>';
  const tags = state.stats.tags || [];
  $('#tag-list').innerHTML = tags.length
    ? tags.map((t) => `<button type="button" class="chip ${state.tag === t.name ? 'active' : ''}" data-tag="${esc(t.name)}"><span class="label"># ${esc(t.name)}</span><span class="num">${t.count}</span></button>`).join('')
    : '<div class="chip-empty">暂无标签</div>';
}

function renderCounts() {
  $('#count-all').textContent = state.stats.total;
  $('#count-today').textContent = state.stats.today || 0;
  $('#count-starred').textContent = state.stats.starred;
  $('#count-due').textContent = state.stats.due;
  $('#view-count').textContent = `${state.items.length} 条`;
}

function renderContext() {
  const titles = { all: '全部备忘录', today: '今天', starred: '收藏', reminders: '待提醒' };
  $('#view-title').textContent = titles[state.view] || '全部备忘录';
  const parts = [];
  if (state.folder) parts.push(`文件夹：${state.folder}`);
  if (state.tag) parts.push(`标签：${state.tag}`);
  if (state.q) parts.push(`搜索：${state.q}`);
  const pill = $('#active-filter');
  pill.hidden = parts.length === 0;
  pill.textContent = parts.join(' · ');
}

function renderList() {
  const list = $('#memo-list');
  if (!state.items.length) {
    list.innerHTML = '';
    $('#empty-state').hidden = false;
    const searching = Boolean(state.q || state.folder || state.tag || state.view !== 'all');
    $('#empty-title').textContent = searching ? '没有匹配的备忘录' : '还没有备忘录';
    $('#empty-desc').textContent = searching
      ? '换个关键词试试，或清空筛选条件。'
      : '点击「开始录音」，或导入一段本地音频文件，也可以新建纯文字备忘录。';
    $('#btn-empty-record').textContent = searching ? '清空筛选' : '开始录音';
    return;
  }
  $('#empty-state').hidden = true;
  list.innerHTML = state.items.map((m, i) => {
    const excerpt = m.summary || m.transcript || m.note || '';
    const [statusText, statusCls] = statusInfo(m.transcriptStatus);
    return `
      <article class="memo-card ${m.id === state.selectedId ? 'selected' : ''}" data-id="${esc(m.id)}" style="animation-delay:${Math.min(i, 8) * 22}ms">
        <div class="card-top">
          <h3>${highlight(m.title, state.q)}</h3>
          <button type="button" class="star-btn ${m.starred ? 'on' : ''}" data-star="${esc(m.id)}" title="收藏">${m.starred ? '★' : '☆'}</button>
        </div>
        <p class="card-excerpt ${excerpt ? '' : 'empty'}">${excerpt ? highlight(excerpt.slice(0, 160), state.q) : '暂无内容，点击查看详情'}</p>
        <div class="card-meta">
          <span>${esc(fmtDate(m.createdAt))}</span><span class="sep">·</span>
          <span>${m.durationMs ? esc(fmtDuration(m.durationMs)) : '无音频'}</span>
          <span class="status-mini ${statusCls}">${statusText}</span>
          ${(m.tags || []).slice(0, 3).map((t) => `<span class="mini-tag">${esc(t)}</span>`).join('')}
          ${m.remindAt ? `<span class="remind-flag">提醒 ${esc(fmtDate(m.remindAt))}</span>` : ''}
        </div>
      </article>`;
  }).join('');
}

function renderDetailOptions() {
  if (!state.current) return;
  $('#folder-options').innerHTML = (state.stats.folders || [])
    .map((f) => `<option value="${esc(f.name)}"></option>`).join('');
  $('#d-tag-suggest').innerHTML = (state.stats.tags || []).slice(0, 12)
    .map((t) => `<button type="button" data-add-tag="${esc(t.name)}">${esc(t.name)}</button>`).join('');
}

/* ---------- 详情与保存 ---------- */
async function selectMemo(id) {
  await saveNow();
  let memo = state.items.find((m) => m.id === id);
  if (!memo) {
    try { memo = await api(`/api/memos/${encodeURIComponent(id)}`); }
    catch (err) { toast(err.message, 'error'); return; }
  }
  state.selectedId = id;
  state.current = memo;
  fillDetail(memo);
  renderList();
  $('#detail').classList.add('open');
}

function clearDetail() {
  clearTimeout(state.saveTimer);
  state.selectedId = null;
  state.current = null;
  $('#detail-empty').hidden = false;
  $('#detail-body').hidden = true;
  $('#detail').classList.remove('open');
  renderList();
}

function setAudio(m, mode) {
  const audio = $('#d-audio');
  const has = Boolean(m.audioFile);
  $('#player-wrap').hidden = !has;
  $('#no-audio').hidden = has;
  if (has) $('#d-audio-meta').textContent = `${fmtBytes(m.audioSize)} · ${m.audioMime || 'audio'}`;
  if (mode === 'keep') return;
  const key = has ? `${m.id}-${m.audioSize}` : '';
  if (mode === 'auto' && audio.dataset.forId === key) return;
  if (has) {
    ensureDurationFix(audio);
    audio.preload = 'auto';
    audio.src = `/api/memos/${encodeURIComponent(m.id)}/audio?t=${Date.now()}`;
    audio.dataset.forId = key;
    // 明确触发资源加载：换 src 后不 load()，部分浏览器会沿用上一次的解码状态，
    // 表现为「第一次点播放没声音、第二次才正常」。
    audio.load();
  } else {
    audio.pause();
    audio.removeAttribute('src');
    audio.load();
    audio.dataset.forId = '';
  }
}

function fillDetail(m, opts = {}) {
  $('#detail-empty').hidden = true;
  $('#detail-body').hidden = false;
  $('#d-title').value = m.title || '';
  $('#d-created').textContent = `创建于 ${fmtDateTime(m.createdAt)}`;
  $('#d-duration').textContent = m.durationMs ? `时长 ${fmtDuration(m.durationMs)}` : '无音频';
  $('#d-star').textContent = m.starred ? '★ 已收藏' : '☆ 收藏';
  $('#d-star').classList.toggle('on', Boolean(m.starred));
  $('#d-summary').value = m.summary || '';
  $('#d-transcript').value = m.transcript || '';
  $('#d-note').value = m.note || '';
  $('#d-folder').value = m.folder || '默认';
  $('#d-tags').value = (m.tags || []).join(', ');
  $('#d-remind').value = toLocalInput(m.remindAt);
  setBadge($('#d-ai-badge'), m.aiStatus);
  setBadge($('#d-asr-badge'), m.transcriptStatus);
  setAudio(m, opts.audio || 'auto');
  renderDetailOptions();
  $('#d-save-state').textContent = '修改自动保存';
}

function collectForm() {
  return {
    title: $('#d-title').value.trim(),
    note: $('#d-note').value,
    transcript: $('#d-transcript').value,
    summary: $('#d-summary').value,
    tags: parseTags($('#d-tags').value),
    folder: $('#d-folder').value.trim() || '默认',
    remindAtLocal: $('#d-remind').value,
  };
}

function diffPatch(m, form) {
  const patch = {};
  if (form.title !== (m.title || '')) patch.title = form.title;
  if (form.note !== (m.note || '')) patch.note = form.note;
  if (form.transcript !== (m.transcript || '')) patch.transcript = form.transcript;
  if (form.summary !== (m.summary || '')) patch.summary = form.summary;
  if (form.folder !== (m.folder || '默认')) patch.folder = form.folder;
  if (JSON.stringify(form.tags) !== JSON.stringify(m.tags || [])) patch.tags = form.tags;
  if (form.remindAtLocal !== toLocalInput(m.remindAt)) {
    patch.remindAt = form.remindAtLocal ? new Date(form.remindAtLocal).toISOString() : null;
  }
  return patch;
}

async function saveNow(opts = {}) {
  clearTimeout(state.saveTimer);
  if (!state.current || !state.selectedId || state.saving) return;
  const form = collectForm();
  if (!form.title) {
    form.title = state.current.title || '未命名备忘录';
    $('#d-title').value = form.title;
  }
  const patch = diffPatch(state.current, form);
  if (!Object.keys(patch).length) return;
  state.saving = true;
  $('#d-save-state').textContent = '保存中…';
  try {
    const updated = await api(`/api/memos/${encodeURIComponent(state.selectedId)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(patch),
    });
    state.current = updated;
    $('#d-save-state').textContent = '已保存';
    if (!opts.silent) toast('已保存', 'ok');
    await refresh();
  } catch (err) {
    $('#d-save-state').textContent = '保存失败，请重试';
    toast(err.message, 'error');
  } finally {
    state.saving = false;
    // 保存期间产生的新修改，等锁释放后补一次保存。
    if (state.current && state.selectedId && Object.keys(diffPatch(state.current, collectForm())).length) {
      state.saveTimer = setTimeout(() => saveNow(), 300);
    }
  }
}

function scheduleSave() {
  $('#d-save-state').textContent = '有未保存的修改';
  clearTimeout(state.saveTimer);
  state.saveTimer = setTimeout(() => saveNow(), 800);
}

/* ---------- 录音 / 导入 ---------- */
const FORMAT_META = {
  wav:  { label: 'WAV · PCM 无损（全平台通用，默认）', hint: '自带完整时长信息，Windows / macOS / iOS / Android / 车载播放器都能直接打开；48kHz 单声道约 5.8 MB/分钟。' },
  webm: { label: 'WebM · Opus（体积小）', hint: 'Chrome / Edge / Firefox 原生支持，体积约为 WAV 的 1/10；部分设备与播放器不识别。' },
  mp4:  { label: 'M4A · AAC（苹果设备友好）', hint: 'iPhone / macOS / QuickTime 直接播放，体积较小；Windows 需较新播放器。' },
  ogg:  { label: 'OGG · Opus（Firefox）', hint: 'Firefox 原生支持，其他平台兼容性一般。' },
  auto: { label: '浏览器默认（不转换）', hint: '直接使用 MediaRecorder 的原始容器，可能缺少时长元数据。' },
};

const MIME_EXT = {
  'audio/webm': 'webm', 'audio/mp4': 'm4a', 'audio/x-m4a': 'm4a', 'audio/ogg': 'ogg',
  'audio/wav': 'wav', 'audio/x-wav': 'wav', 'audio/mpeg': 'mp3', 'audio/aac': 'aac',
  'audio/flac': 'flac',
};

function extOf(mime) { return MIME_EXT[String(mime || '').split(';')[0].toLowerCase()] || 'bin'; }

function supportedFormats() {
  const list = [{ value: 'wav', mime: '' }];
  if (typeof MediaRecorder === 'undefined') return list;
  const add = (value, candidates) => {
    for (const type of candidates) {
      try {
        if (MediaRecorder.isTypeSupported(type)) { list.push({ value, mime: type }); return; }
      } catch (_) { /* skip */ }
    }
  };
  add('webm', ['audio/webm;codecs=opus', 'audio/webm']);
  add('mp4', ['audio/mp4', 'audio/mp4;codecs=mp4a.40.2']);
  add('ogg', ['audio/ogg;codecs=opus', 'audio/ogg']);
  list.push({ value: 'auto', mime: '' });
  return list;
}

// 采集用的 MIME：WAV 无法由 MediaRecorder 直接产出，先用浏览器原生格式采集再转码。
function captureMime(format) {
  const list = supportedFormats();
  const target = list.find((f) => f.value === format && f.mime);
  if (target) return target.mime;
  const first = list.find((f) => f.mime);
  return first ? first.mime : '';
}

async function decodeAudio(blob) {
  const AC = window.AudioContext || window.webkitAudioContext;
  if (!AC) throw new Error('当前浏览器不支持 Web Audio 转码');
  const ctx = new AC();
  try { return await ctx.decodeAudioData(await blob.arrayBuffer()); }
  finally { try { await ctx.close(); } catch (_) { /* ignore */ } }
}

// 16-bit PCM WAV：写全 RIFF 头（含 data 长度），播放器一开始就能算出 duration。
function encodeWav(buffer) {
  const channels = Math.min(2, buffer.numberOfChannels);
  const rate = buffer.sampleRate;
  const frames = buffer.length;
  const dataBytes = frames * channels * 2;
  const total = 44 + dataBytes;
  const view = new DataView(new ArrayBuffer(total));
  const str = (off, s) => { for (let i = 0; i < s.length; i += 1) view.setUint8(off + i, s.charCodeAt(i)); };
  str(0, 'RIFF'); view.setUint32(4, total - 8, true); str(8, 'WAVE');
  str(12, 'fmt '); view.setUint32(16, 16, true); view.setUint16(20, 1, true);
  view.setUint16(22, channels, true); view.setUint32(24, rate, true);
  view.setUint32(28, rate * channels * 2, true); view.setUint16(32, channels * 2, true);
  view.setUint16(34, 16, true);
  str(36, 'data'); view.setUint32(40, dataBytes, true);
  const chans = [];
  for (let c = 0; c < channels; c += 1) chans.push(buffer.getChannelData(c));
  let off = 44;
  for (let i = 0; i < frames; i += 1) {
    for (let c = 0; c < channels; c += 1) {
      const s = Math.max(-1, Math.min(1, chans[c][i]));
      view.setInt16(off, s < 0 ? s * 0x8000 : s * 0x7fff, true);
      off += 2;
    }
  }
  return new Blob([view.buffer], { type: 'audio/wav' });
}

// Chrome 的 MediaRecorder 产出的 WebM 常缺 EBML Duration，首次播放 duration=Infinity 会静音。
// 触发一次「seek 到极大值」可让浏览器扫描出真实时长；WAV/M4A 不会命中此分支。
function ensureDurationFix(audio) {
  if (audio.dataset.durationFixed) return;
  audio.dataset.durationFixed = '1';
  audio.addEventListener('loadedmetadata', () => {
    if (!Number.isFinite(audio.duration) || audio.duration === Infinity) {
      audio.currentTime = 1e101;
      const restore = () => { try { audio.currentTime = 0; } catch (_) { /* ignore */ } };
      audio.addEventListener('timeupdate', restore, { once: true });
      audio.addEventListener('seeked', restore, { once: true });
    }
  });
}

function audioFilename(m, ext) {
  const base = String(m.title || '语音备忘录').replace(/[\\/:*?"<>|]/g, '_').slice(0, 60);
  return `${base}.${ext}`;
}

function triggerDownload(url, filename) {
  const a = document.createElement('a');
  a.href = url; a.download = filename;
  document.body.appendChild(a); a.click(); a.remove();
}

async function downloadAudio(forceWav) {
  const m = state.current;
  if (!m || !m.audioFile) { toast('这条备忘录还没有音频', 'warn'); return; }
  const url = `/api/memos/${encodeURIComponent(m.id)}/audio`;
  const wantWav = Boolean(forceWav) || state.downloadFormat === 'wav';
  const isWav = String(m.audioMime || '').includes('wav');
  if (!wantWav || isWav) {
    triggerDownload(url, audioFilename(m, extOf(m.audioMime)));
    return;
  }
  toast('正在转换为 WAV，请稍候…');
  try {
    const res = await fetch(url);
    if (!res.ok) throw new Error('读取音频失败');
    const buf = await decodeAudio(await res.blob());
    const wav = encodeWav(buf);
    const obj = URL.createObjectURL(wav);
    triggerDownload(obj, audioFilename(m, 'wav'));
    setTimeout(() => URL.revokeObjectURL(obj), 30000);
    toast('WAV 已开始下载', 'ok');
  } catch (err) {
    toast(`转码失败，可直接下载原始格式：${err.message}`, 'warn');
    triggerDownload(url, audioFilename(m, extOf(m.audioMime)));
  }
}

function startWave(stream) {
  try {
    const AC = window.AudioContext || window.webkitAudioContext;
    if (!AC || !state.rec) return;
    const ctx = new AC();
    const analyser = ctx.createAnalyser();
    analyser.fftSize = 2048;
    ctx.createMediaStreamSource(stream).connect(analyser);
    const canvas = $('#wave');
    const c = canvas.getContext('2d');
    const data = new Uint8Array(analyser.fftSize);
    const wave = { ctx, raf: 0 };
    state.rec.wave = wave;
    const draw = () => {
      if (!wave || !state.rec) return;
      const w = canvas.width;
      const h = canvas.height;
      analyser.getByteTimeDomainData(data);
      c.clearRect(0, 0, w, h);
      c.fillStyle = '#f4f5fb';
      c.fillRect(0, 0, w, h);
      c.strokeStyle = 'rgba(91,91,214,.18)';
      c.lineWidth = 1;
      c.beginPath(); c.moveTo(0, h / 2); c.lineTo(w, h / 2); c.stroke();
      c.beginPath();
      for (let x = 0; x < w; x += 2) {
        const v = data[Math.floor((x / w) * data.length)] / 128 - 1;
        const y = h / 2 + v * h * 0.42;
        if (x === 0) c.moveTo(x, y); else c.lineTo(x, y);
      }
      c.strokeStyle = '#5b5bd6';
      c.lineWidth = 2;
      c.stroke();
      wave.raf = requestAnimationFrame(draw);
    };
    draw();
  } catch (_) { /* 波形为增强能力，失败不影响录音 */ }
}

function stopWave(wave) {
  if (!wave) return;
  cancelAnimationFrame(wave.raf);
  try { wave.ctx.close(); } catch (_) { /* ignore */ }
}

async function startRecording() {
  if (state.rec) { stopRecording(); return; }
  if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia || typeof MediaRecorder === 'undefined') {
    const hint = window.isSecureContext
      ? '请用最新版 Chrome / Edge / Safari，或改用「导入音频」'
      : '当前为 HTTP 访问，浏览器已禁用麦克风；请开启内置 HTTPS / 按提示配置浏览器，或改用「导入音频」';
    toast(`当前环境不支持浏览器录音：${hint}`, 'warn');
    return;
  }
  let stream;
  try { stream = await navigator.mediaDevices.getUserMedia({ audio: true }); }
  catch (err) { toast(`无法访问麦克风：${err.message || '权限被拒绝'}`, 'error'); return; }

  const available = supportedFormats();
  let targetFormat = state.recordFormat;
  if (!available.some((f) => f.value === targetFormat)) {
    toast(`当前浏览器不支持 ${targetFormat}，本次改用浏览器默认格式`, 'warn');
    targetFormat = 'auto';
  }
  const mime = captureMime(targetFormat);
  let recorder;
  try { recorder = mime ? new MediaRecorder(stream, { mimeType: mime }) : new MediaRecorder(stream); }
  catch (err) {
    stream.getTracks().forEach((t) => t.stop());
    toast(`无法启动录音：${err.message}`, 'error');
    return;
  }

  const rec = {
    stream, recorder, mime, targetFormat, chunks: [], startedAt: Date.now(),
    cancelled: false, timer: null, wave: null,
  };
  state.rec = rec;
  recorder.ondataavailable = (e) => { if (e.data && e.data.size) rec.chunks.push(e.data); };
  recorder.onstop = async () => {
    state.rec = null;
    clearInterval(rec.timer);
    stopWave(rec.wave);
    rec.stream.getTracks().forEach((t) => t.stop());
    $('#recorder-panel').hidden = true;
    $('#btn-record').classList.remove('recording');
    $('#btn-record-label').textContent = '开始录音';
    $('#btn-stop').disabled = false;
    if (rec.cancelled) { toast('已取消录音'); return; }
    const raw = new Blob(rec.chunks, { type: rec.mime || 'audio/webm' });
    if (!raw.size) { toast('未采集到音频数据', 'warn'); return; }

    let outBlob = raw;
    let outMime = String(rec.mime || 'audio/webm').split(';')[0];
    let durationMs = Date.now() - rec.startedAt;
    try {
      // 先解码一次拿到精确时长（MediaRecorder 的计时与实际音频长度常有偏差）。
      const buffer = await decodeAudio(raw);
      if (buffer.duration > 0 && Number.isFinite(buffer.duration)) {
        durationMs = Math.round(buffer.duration * 1000);
      }
      if (rec.targetFormat === 'wav') {
        const estimated = buffer.length * Math.min(2, buffer.numberOfChannels) * 2;
        if (estimated > 400 * 1024 * 1024) {
          toast('录音较长，转 WAV 会超出浏览器内存上限，已按原始格式保存', 'warn');
        } else {
          outBlob = encodeWav(buffer);
          outMime = 'audio/wav';
        }
      }
    } catch (err) {
      if (rec.targetFormat === 'wav') {
        toast(`WAV 转码失败，已按原始格式保存：${err.message}`, 'warn');
      }
    }
    await finalizeAudio(outBlob, outMime, durationMs);
  };

  $('#recorder-panel').hidden = false;
  $('#btn-record').classList.add('recording');
  $('#btn-record-label').textContent = '录音中…';
  $('#rec-state').textContent = '正在录音…';
  $('#rec-time').textContent = '00:00';
  rec.timer = setInterval(() => {
    if (state.rec) $('#rec-time').textContent = fmtDuration(Date.now() - rec.startedAt);
  }, 250);
  startWave(stream);
  recorder.start(250);
}

function stopRecording() {
  if (!state.rec) return;
  $('#rec-state').textContent = '正在保存…';
  $('#btn-stop').disabled = true;
  try { state.rec.recorder.stop(); }
  catch (err) { toast(err.message, 'error'); $('#btn-stop').disabled = false; }
}

function cancelRecording() {
  if (!state.rec) return;
  state.rec.cancelled = true;
  try { state.rec.recorder.stop(); } catch (_) { /* ignore */ }
}

function normalizeMime(file) {
  const type = (file.type || '').split(';')[0].toLowerCase();
  if (type.startsWith('audio/')) return type;
  const ext = (file.name.split('.').pop() || '').toLowerCase();
  const map = {
    webm: 'audio/webm', mp4: 'audio/mp4', m4a: 'audio/mp4', ogg: 'audio/ogg',
    wav: 'audio/wav', mp3: 'audio/mpeg', aac: 'audio/aac', flac: 'audio/flac',
  };
  return map[ext] || '';
}

function readDuration(file) {
  return new Promise((resolve) => {
    const url = URL.createObjectURL(file);
    const audio = new Audio();
    const done = (value) => { URL.revokeObjectURL(url); resolve(value); };
    audio.preload = 'metadata';
    audio.onloadedmetadata = () => done(Number.isFinite(audio.duration) ? audio.duration * 1000 : 0);
    audio.onerror = () => done(0);
    audio.src = url;
  });
}

async function finalizeAudio(blob, mime, durationMs, existingId = null) {
  toast('正在保存音频…');
  try {
    let memo;
    if (existingId) {
      memo = state.items.find((m) => m.id === existingId)
        || await api(`/api/memos/${encodeURIComponent(existingId)}`);
    } else {
      const body = { durationMs: Math.round(durationMs) };
      if (state.folder) body.folder = state.folder;
      memo = await api('/api/memos', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
    }
    const res = await fetch(`/api/memos/${encodeURIComponent(memo.id)}/audio`, {
      method: 'PUT',
      headers: {
        'Content-Type': mime,
        'x-audio-mime': mime,
        'x-audio-duration': String(Math.round(durationMs)),
      },
      body: blob,
    });
    const data = await res.json().catch(() => null);
    if (!res.ok) throw new Error((data && data.error) || '音频上传失败');
    state.selectedId = memo.id;
    state.current = data;
    await refresh();
    fillDetail(data, { audio: 'reload' });
    $('#detail').classList.add('open');
    toast('音频已保存', 'ok');
    if (state.config.asr) await runTranscribe();
    else toast('未配置转写服务，可在详情里直接粘贴转写文本');
  } catch (err) {
    toast(err.message, 'error');
  }
}

async function importFile(file) {
  if (!file) return;
  const mime = normalizeMime(file);
  if (!mime) { toast('不支持的音频格式', 'warn'); return; }
  const durationMs = await readDuration(file);
  await finalizeAudio(file, mime, durationMs);
}

/* ---------- AI 能力 ---------- */
async function runTranscribe() {
  if (!state.current || !state.selectedId) { toast('请先选择一条备忘录', 'warn'); return; }
  if (!state.current.audioFile) { toast('这条备忘录还没有音频', 'warn'); return; }
  await saveNow();
  const btn = $('#d-transcribe');
  btn.disabled = true;
  setBadge($('#d-asr-badge'), 'pending');
  try {
    const updated = await api(`/api/memos/${encodeURIComponent(state.selectedId)}/transcribe`, { method: 'POST' });
    state.current = updated;
    fillDetail(updated, { audio: 'keep' });
    await refresh();
    toast('转写完成', 'ok');
    if (state.config.llm) await runAnalyze();
  } catch (err) {
    if (err.data && err.data.memo) {
      state.current = err.data.memo;
      setBadge($('#d-asr-badge'), err.data.memo.transcriptStatus);
    }
    toast(err.message, err.code === 'NO_ASR' ? 'warn' : 'error');
    if (err.code === 'NO_ASR') $('#d-transcript').focus();
    await refresh();
  } finally {
    btn.disabled = false;
  }
}

async function runAnalyze() {
  if (!state.current || !state.selectedId) return;
  await saveNow();
  const btn = $('#d-analyze');
  btn.disabled = true;
  setBadge($('#d-ai-badge'), 'pending');
  try {
    const updated = await api(`/api/memos/${encodeURIComponent(state.selectedId)}/analyze`, { method: 'POST' });
    state.current = updated;
    fillDetail(updated, { audio: 'keep' });
    await refresh();
    toast('摘要与标签已生成', 'ok');
  } catch (err) {
    if (err.data && err.data.memo) {
      state.current = err.data.memo;
      setBadge($('#d-ai-badge'), err.data.memo.aiStatus);
    }
    toast(err.message, (err.code === 'NO_LLM' || err.code === 'NO_TRANSCRIPT') ? 'warn' : 'error');
    await refresh();
  } finally {
    btn.disabled = false;
  }
}

/* ---------- 其它操作 ---------- */
function exportIds(ids) {
  if (!ids || !ids.length) { toast('当前没有可导出的备忘录', 'warn'); return; }
  const link = document.createElement('a');
  link.href = `/api/export?ids=${ids.map(encodeURIComponent).join(',')}`;
  document.body.appendChild(link);
  link.click();
  link.remove();
  toast(`正在导出 ${ids.length} 条备忘录`);
}

async function starCurrent() {
  if (!state.current || !state.selectedId) return;
  try {
    const updated = await api(`/api/memos/${encodeURIComponent(state.selectedId)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ starred: !state.current.starred }),
    });
    state.current = updated;
    $('#d-star').textContent = updated.starred ? '★ 已收藏' : '☆ 收藏';
    $('#d-star').classList.toggle('on', Boolean(updated.starred));
    await refresh();
  } catch (err) { toast(err.message, 'error'); }
}

async function deleteCurrent() {
  if (!state.selectedId || !state.current) return;
  if (!window.confirm(`确定删除「${state.current.title}」吗？音频与文本都会被移除，且不可撤销。`)) return;
  clearTimeout(state.saveTimer);
  try {
    await api(`/api/memos/${encodeURIComponent(state.selectedId)}`, { method: 'DELETE' });
    toast('已删除', 'ok');
    clearDetail();
    await refresh();
  } catch (err) { toast(err.message, 'error'); }
}

async function createBlank(folderName = '') {
  await saveNow();
  try {
    const body = { title: '新建备忘录' };
    if (folderName) body.folder = folderName;
    else if (state.folder) body.folder = state.folder;
    const memo = await api('/api/memos', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    state.selectedId = memo.id;
    state.current = memo;
    await refresh();
    fillDetail(memo);
    $('#detail').classList.add('open');
    $('#d-title').focus();
    $('#d-title').select();
  } catch (err) { toast(err.message, 'error'); }
}

async function checkReminders() {
  try {
    const data = await api('/api/reminders');
    const fresh = (data.items || []).filter((m) => !state.notified.has(m.id));
    if (!fresh.length) return;
    fresh.forEach((m) => state.notified.add(m.id));
    const first = fresh[0];
    const banner = $('#reminder-banner');
    banner.hidden = false;
    banner.innerHTML = `
      <span>有 <strong>${fresh.length}</strong> 条备忘录到提醒时间了：${esc(first.title)}</span>
      <span class="banner-actions">
        <button type="button" data-remind-view="${esc(first.id)}">查看</button>
        <button type="button" data-remind-done="1">知道了</button>
        <button type="button" data-remind-notify="1">开启桌面通知</button>
      </span>`;
    if ('Notification' in window && Notification.permission === 'granted') {
      try { new Notification('语音备忘录提醒', { body: first.title }); } catch (_) { /* ignore */ }
    }
  } catch (_) { /* 提醒轮询失败时静默 */ }
}

async function dismissReminders() {
  try {
    const data = await api('/api/reminders');
    for (const m of data.items || []) {
      await api(`/api/reminders/${encodeURIComponent(m.id)}/done`, { method: 'POST' });
    }
  } catch (_) { /* ignore */ }
  $('#reminder-banner').hidden = true;
  await refresh();
}

/* ---------- 事件绑定 ---------- */
/* ---------- 设置中心 ---------- */
function chromeFlagCmd() {
  const origin = location.origin;
  const win = /Windows/i.test(navigator.userAgent);
  const profile = win ? '%TEMP%\\voice-memo-chrome' : '/tmp/voice-memo-chrome';
  return `--unsafely-treat-insecure-origin-as-secure=${origin} --user-data-dir=${profile}`;
}

function copyText(text) {
  const done = () => toast('已复制到剪贴板', 'ok');
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text).then(done).catch(() => fallbackCopy(text, done));
  } else fallbackCopy(text, done);
}

function fallbackCopy(text, done) {
  const ta = document.createElement('textarea');
  ta.value = text; ta.style.position = 'fixed'; ta.style.opacity = '0';
  document.body.appendChild(ta); ta.select();
  try { document.execCommand('copy'); done(); }
  catch (_) { toast('复制失败，请手动选中命令文本', 'warn'); }
  ta.remove();
}

function checkSecureContext() {
  const cmd = chromeFlagCmd();
  $('#insecure-cmd').textContent = cmd;
  $('#set-https-cmd').textContent = cmd;
  let dismissed = false;
  try { dismissed = sessionStorage.getItem('vms-http-hint') === 'off'; } catch (_) { /* file:// 下可能不可用 */ }
  $('#insecure-banner').hidden = window.isSecureContext || dismissed;
}

function fillProviders(s) {
  const fill = (sel, list, current) => {
    const el = $(sel);
    el.innerHTML = list.map((p) => `<option value="${esc(p.id)}">${esc(p.name)}</option>`).join('');
    if (current && Array.from(el.options).some((o) => o.value === current)) el.value = current;
  };
  fill('#set-llm-provider', state.providers.llm || [], s.ai && s.ai.provider);
  fill('#set-asr-provider', state.providers.asr || [], s.ai && s.ai.asrProvider);
}

function syncProviderNote(which, applyValues) {
  const llm = which === 'llm';
  const sel = $(llm ? '#set-llm-provider' : '#set-asr-provider');
  const list = llm ? (state.providers.llm || []) : (state.providers.asr || []);
  const p = list.find((x) => x.id === sel.value);
  $(llm ? '#set-llm-note' : '#set-asr-note').textContent = p ? p.note : '';
  // 只有用户主动切换下拉时才套用预设，避免打开设置就把自定义地址覆盖掉。
  if (!applyValues || !p || !p.baseUrl) return;
  $(llm ? '#set-llm-base' : '#set-asr-base').value = p.baseUrl;
  if (p.model) $(llm ? '#set-llm-model' : '#set-asr-model').value = p.model;
}

function fillRecordForm(s) {
  const list = supportedFormats();
  const sel = $('#set-record-format');
  sel.innerHTML = list.map((f) => `<option value="${esc(f.value)}">${esc(FORMAT_META[f.value].label)}</option>`).join('');
  const want = (s.record && s.record.format) || 'wav';
  sel.value = Array.from(sel.options).some((o) => o.value === want) ? want : 'wav';
  const hint = () => { $('#set-record-hint').textContent = FORMAT_META[sel.value] ? FORMAT_META[sel.value].hint : ''; };
  hint();
  sel.onchange = hint;
  $('#set-record-capability').textContent = `当前浏览器可用容器：${list.map((f) => f.value).join(' / ')}（WAV 由浏览器转码生成）`;
  $('#set-download-format').value = (s.record && s.record.downloadFormat) || 'wav';
}

function fillRuntime(s) {
  const r = s.runtime || {};
  const l = s.limits || {};
  $('#set-max-audio').value = l.maxAudioMB || 0;
  $('#set-max-export').value = l.maxExportMB || 0;
  $('#set-max-items').value = l.maxExportItems || 0;
  $('#set-runtime').innerHTML = [
    `运行身份 PUID / PGID：<b>${esc(r.puid)} / ${esc(r.pgid)}</b>`,
    `UMASK：<b>${esc(r.umask)}</b>`,
    `数据目录：<b>${esc(r.dataDir)}</b>`,
    `内置 HTTPS：<b>${r.https ? '已开启' : '未开启'}</b>`,
    `证书 SAN：<b>${esc(r.tlsSan || 'localhost + 本机地址')}</b>`,
  ].join('<br>');

  const secure = Boolean(window.isSecureContext);
  $('#set-https-state').innerHTML = [
    `访问地址：<b>${esc(location.origin)}</b>`,
    `协议：<b>${esc(location.protocol)}</b>`,
    `浏览器安全上下文：<b>${secure ? '是（可录音）' : '否（麦克风被禁用）'}</b>`,
    `套件内置 HTTPS：<b>${r.https ? '已开启' : '未开启'}</b>`,
  ].join('<br>');
  $('#set-secure-state').textContent = secure
    ? '当前连接可用麦克风。'
    : '浏览器只允许 HTTPS / localhost 使用麦克风；若已用启动参数标记为安全源仍显示「否」，请确认使用了独立的 user-data-dir 并重启浏览器。';
  $('#set-ca-link').hidden = !r.https;
  $('#set-https-guide').textContent = r.https
    ? '导入 CA 证书到系统/浏览器信任库后，地址栏不再告警，麦克风可正常使用；也可换成 NAS 已有正式证书的反向代理。'
    : '开启方法：在 docker-compose.yml 的 environment 加 ENABLE_HTTPS=1 与 TLS_SAN=<NAS IP 或域名>，重建容器后用 https:// 访问同一端口。';
}

async function openSettings(tab) {
  try {
    const s = await api('/api/settings');
    state.settings = s;
    state.providers = s.providers || { llm: [], asr: [] };
    const ai = s.ai || {};
    fillProviders(s);
    $('#set-llm-base').value = ai.baseUrl || '';
    $('#set-llm-model').value = ai.model || '';
    $('#set-llm-path').value = ai.chatPath || '/chat/completions';
    $('#set-asr-base').value = ai.asrBaseUrl || '';
    $('#set-asr-model').value = ai.asrModel || '';
    $('#set-asr-language').value = ai.asrLanguage || 'zh';
    for (const sel of ['#set-llm-key', '#set-asr-key']) {
      $(sel).value = '';
      $(sel).dataset.cleared = '';
      $(sel).placeholder = '不修改请留空';
    }
    $('#set-llm-key').placeholder = ai.apiKeySet ? `已保存：${ai.apiKeyHint}（留空不改）` : '未设置，填入密钥';
    $('#set-asr-key').placeholder = ai.asrApiKeySet ? `已保存：${ai.asrApiKeyHint}（留空不改）` : '未设置，填入密钥';
    $('#set-llm-test-result').textContent = '';
    $('#set-asr-test-result').textContent = '';
    $('#settings-status').textContent = '';
    syncProviderNote('llm');
    syncProviderNote('asr');
    fillRecordForm(s);
    fillRuntime(s);
    switchTab(tab || 'ai');
    $('#settings-modal').hidden = false;
  } catch (err) {
    toast(`读取设置失败：${err.message}`, 'error');
  }
}

function closeSettings() { $('#settings-modal').hidden = true; }

function switchTab(tab) {
  $$('#settings-tabs .modal-tab').forEach((b) => b.classList.toggle('active', b.dataset.tab === tab));
  $$('.settings-panel').forEach((p) => p.classList.toggle('active', p.dataset.panel === tab));
}

function keyPatch(selector) {
  const el = $(selector);
  if (el.dataset.cleared === '1') return '';
  const v = el.value.trim();
  return v === '' ? '__KEEP__' : v;
}

function collectSettings() {
  return {
    ai: {
      provider: $('#set-llm-provider').value,
      baseUrl: $('#set-llm-base').value.trim(),
      model: $('#set-llm-model').value.trim(),
      chatPath: $('#set-llm-path').value.trim() || '/chat/completions',
      apiKey: keyPatch('#set-llm-key'),
      asrProvider: $('#set-asr-provider').value,
      asrBaseUrl: $('#set-asr-base').value.trim(),
      asrModel: $('#set-asr-model').value.trim(),
      asrPath: '/audio/transcriptions',
      asrLanguage: $('#set-asr-language').value.trim(),
      asrApiKey: keyPatch('#set-asr-key'),
    },
    limits: {
      maxAudioMB: Math.max(0, Number($('#set-max-audio').value) || 0),
      maxExportMB: Math.max(0, Number($('#set-max-export').value) || 0),
      maxExportItems: Math.max(0, Number($('#set-max-items').value) || 0),
    },
    record: {
      format: $('#set-record-format').value,
      downloadFormat: $('#set-download-format').value,
    },
  };
}

async function saveSettings(silent) {
  const btn = $('#settings-save');
  btn.disabled = true;
  $('#settings-status').textContent = '保存中…';
  try {
    const saved = await api('/api/settings', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(collectSettings()),
    });
    state.settings = saved;
    state.recordFormat = saved.record.format;
    state.downloadFormat = saved.record.downloadFormat;
    await loadConfig();
    $('#settings-status').textContent = '已保存并生效';
    if (!silent) toast('设置已保存，立即生效', 'ok');
    for (const sel of ['#set-llm-key', '#set-asr-key']) { $(sel).value = ''; $(sel).dataset.cleared = ''; }
    return true;
  } catch (err) {
    $('#settings-status').textContent = `保存失败：${err.message}`;
    if (!silent) toast(err.message, 'error');
    return false;
  } finally {
    btn.disabled = false;
  }
}

async function testAI(target) {
  const el = $(target === 'asr' ? '#set-asr-test-result' : '#set-llm-test-result');
  el.className = 'test-result';
  el.textContent = '正在保存并测试…';
  if (!(await saveSettings(true))) { el.className = 'test-result bad'; el.textContent = '设置保存失败'; return; }
  try {
    const r = await api('/api/ai/test', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ target }),
    });
    el.className = `test-result ${r.ok ? 'ok' : 'bad'}`;
    el.textContent = r.message || (r.ok ? '连接成功' : '连接失败');
  } catch (err) {
    el.className = 'test-result bad';
    el.textContent = err.message;
  }
}

function bindSettingsEvents() {
  $('#btn-settings').addEventListener('click', () => openSettings());
  $('#settings-close').addEventListener('click', closeSettings);
  $('#settings-modal').addEventListener('click', (e) => { if (e.target.id === 'settings-modal') closeSettings(); });
  $('#settings-tabs').addEventListener('click', (e) => {
    const btn = e.target.closest('[data-tab]');
    if (btn) switchTab(btn.dataset.tab);
  });
  $('#settings-save').addEventListener('click', () => saveSettings(false));
  $('#settings-reload').addEventListener('click', () => { openSettings(); toast('已重新载入服务端设置'); });
  $('#set-llm-provider').addEventListener('change', () => syncProviderNote('llm', true));
  $('#set-asr-provider').addEventListener('change', () => syncProviderNote('asr', true));
  for (const [clearSel, keySel] of [['#set-llm-clear', '#set-llm-key'], ['#set-asr-clear', '#set-asr-key']]) {
    $(clearSel).addEventListener('click', () => {
      $(keySel).value = '';
      $(keySel).dataset.cleared = '1';
      toast('将在保存后清除该密钥，请先点「保存设置」', 'warn');
    });
    $(keySel).addEventListener('input', () => { $(keySel).dataset.cleared = ''; });
  }
  $('#set-llm-test').addEventListener('click', () => testAI('llm'));
  $('#set-asr-test').addEventListener('click', () => testAI('asr'));
  $('#d-download').addEventListener('click', () => downloadAudio(false));
  $('#d-download-wav').addEventListener('click', () => downloadAudio(true));
  $('#insecure-copy').addEventListener('click', () => copyText(chromeFlagCmd()));
  $('#set-https-copy').addEventListener('click', () => copyText(chromeFlagCmd()));
  $('#insecure-open-settings').addEventListener('click', () => openSettings('https'));
  $('#insecure-dismiss').addEventListener('click', () => {
    $('#insecure-banner').hidden = true;
    try { sessionStorage.setItem('vms-http-hint', 'off'); } catch (_) { /* ignore */ }
  });
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && !$('#settings-modal').hidden) closeSettings();
  });
}

function bindEvents() {
  $('#btn-record').addEventListener('click', () => { if (state.rec) stopRecording(); else startRecording(); });
  $('#btn-empty-record').addEventListener('click', () => {
    if (state.q || state.folder || state.tag || state.view !== 'all') {
      state.q = ''; state.folder = ''; state.tag = ''; state.view = 'all';
      $('#search').value = '';
      $('#search-clear').hidden = true;
      refresh();
    } else startRecording();
  });
  $('#btn-import').addEventListener('click', () => { state.replaceTarget = null; $('#file-input').click(); });
  $('#btn-new').addEventListener('click', () => createBlank());
  $('#btn-add-folder').addEventListener('click', () => {
    const name = window.prompt('新文件夹名称（将同时创建一条空白备忘录）');
    if (name && name.trim()) createBlank(name.trim());
  });
  $('#file-input').addEventListener('change', (e) => {
    const file = e.target.files && e.target.files[0];
    e.target.value = '';
    if (!file) return;
    const target = state.replaceTarget;
    state.replaceTarget = null;
    if (target) {
      const mime = normalizeMime(file);
      if (!mime) { toast('不支持的音频格式', 'warn'); return; }
      readDuration(file).then((d) => finalizeAudio(file, mime, d, target));
    } else importFile(file);
  });

  $('#nav-views').addEventListener('click', (e) => {
    const btn = e.target.closest('[data-view]');
    if (!btn) return;
    state.view = btn.dataset.view;
    state.folder = '';
    state.tag = '';
    refresh();
  });
  $('#folder-list').addEventListener('click', (e) => {
    const btn = e.target.closest('[data-folder]');
    if (!btn) return;
    state.folder = state.folder === btn.dataset.folder ? '' : btn.dataset.folder;
    state.tag = '';
    state.view = 'all';
    refresh();
  });
  $('#tag-list').addEventListener('click', (e) => {
    const btn = e.target.closest('[data-tag]');
    if (!btn) return;
    state.tag = state.tag === btn.dataset.tag ? '' : btn.dataset.tag;
    state.folder = '';
    state.view = 'all';
    refresh();
  });

  $('#search').addEventListener('input', (e) => {
    $('#search-clear').hidden = !e.target.value;
    clearTimeout(state.searchTimer);
    state.searchTimer = setTimeout(() => { state.q = e.target.value.trim(); refresh(); }, 220);
  });
  $('#search-clear').addEventListener('click', () => {
    $('#search').value = '';
    $('#search-clear').hidden = true;
    state.q = '';
    refresh();
  });
  $('#sort').addEventListener('change', (e) => { state.sort = e.target.value; refresh(); });
  $('#btn-export-all').addEventListener('click', () => exportIds(state.items.map((m) => m.id)));

  $('#btn-stop').addEventListener('click', stopRecording);
  $('#btn-cancel-rec').addEventListener('click', cancelRecording);

  $('#memo-list').addEventListener('click', async (e) => {
    const star = e.target.closest('[data-star]');
    if (star) {
      e.stopPropagation();
      const m = state.items.find((x) => x.id === star.dataset.star);
      if (!m) return;
      try {
        const updated = await api(`/api/memos/${encodeURIComponent(m.id)}`, {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ starred: !m.starred }),
        });
        if (state.selectedId === m.id) {
          state.current = updated;
          $('#d-star').textContent = updated.starred ? '★ 已收藏' : '☆ 收藏';
          $('#d-star').classList.toggle('on', Boolean(updated.starred));
        }
        await refresh();
      } catch (err) { toast(err.message, 'error'); }
      return;
    }
    const card = e.target.closest('[data-id]');
    if (card) selectMemo(card.dataset.id);
  });

  ['#d-title', '#d-note', '#d-transcript', '#d-summary', '#d-folder', '#d-tags', '#d-remind'].forEach((sel) => {
    $(sel).addEventListener('input', scheduleSave);
    $(sel).addEventListener('change', scheduleSave);
  });
  $('#d-save').addEventListener('click', () => saveNow({ silent: false }));
  $('#d-star').addEventListener('click', starCurrent);
  $('#d-export').addEventListener('click', () => state.selectedId && exportIds([state.selectedId]));
  $('#d-delete').addEventListener('click', deleteCurrent);
  $('#d-close').addEventListener('click', clearDetail);
  $('#d-transcribe').addEventListener('click', runTranscribe);
  $('#d-analyze').addEventListener('click', runAnalyze);
  $('#d-replace-audio').addEventListener('click', () => {
    if (!state.selectedId) return;
    state.replaceTarget = state.selectedId;
    $('#file-input').click();
  });

  $('#d-tag-suggest').addEventListener('click', (e) => {
    const btn = e.target.closest('[data-add-tag]');
    if (!btn) return;
    const tags = parseTags($('#d-tags').value);
    if (!tags.includes(btn.dataset.addTag)) tags.push(btn.dataset.addTag);
    $('#d-tags').value = tags.join(', ');
    scheduleSave();
  });
  $$('.quick-remind button').forEach((btn) => btn.addEventListener('click', () => {
    const input = $('#d-remind');
    if (btn.dataset.clear) input.value = '';
    else if (btn.dataset.tomorrow) {
      const d = new Date();
      d.setDate(d.getDate() + 1);
      d.setHours(9, 0, 0, 0);
      input.value = toLocalInput(d.toISOString());
    } else {
      input.value = toLocalInput(new Date(Date.now() + Number(btn.dataset.min) * 60000).toISOString());
    }
    scheduleSave();
  }));

  $('#reminder-banner').addEventListener('click', async (e) => {
    const view = e.target.closest('[data-remind-view]');
    if (view) {
      $('#reminder-banner').hidden = true;
      await selectMemo(view.dataset.remindView);
      try {
        await api(`/api/reminders/${encodeURIComponent(view.dataset.remindView)}/done`, { method: 'POST' });
        await refresh();
      } catch (_) { /* ignore */ }
      return;
    }
    if (e.target.closest('[data-remind-done]')) { await dismissReminders(); return; }
    if (e.target.closest('[data-remind-notify]')) {
      if (!('Notification' in window)) { toast('当前浏览器不支持桌面通知', 'warn'); return; }
      try {
        const result = await Notification.requestPermission();
        toast(result === 'granted' ? '桌面通知已开启' : '未授予通知权限', result === 'granted' ? 'ok' : 'warn');
      } catch (_) { toast('无法开启桌面通知', 'warn'); }
    }
  });
}

async function init() {
  bindEvents();
  bindSettingsEvents();
  checkSecureContext();
  try {
    await loadConfig();
    await refresh();
    checkReminders();
    setInterval(checkReminders, 30000);
  } catch (err) {
    toast(`初始化失败：${err.message}`, 'error');
  }
}

init();

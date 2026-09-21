// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

export {};

type ExamConfig = {
  exam: {
    id: string;
    hashtag?: string;
    origin: string;
    source_url?: string;
    source_origin?: string;
    source_host?: string;
    proxy_origin: string;
    unlock_path: string;
    state?: string;
    starts_at?: string | null;
    ends_at?: string | null;
  };
  oidc: {authorization_endpoint: string};
  policy: {
    alg: string;
    key_id: string;
    document: {
      exam_id?: string;
      allowed_origins?: string[];
      navigation?: {allowed_origins?: string[]};
      browser?: {require_fullscreen?: boolean; lock_fullscreen?: boolean};
      session?: {heartbeat_seconds?: number};
    };
    signature: string;
  };
  tunnel?: {
    endpoint?: string;
    endpoint_id?: string;
    ticket_path?: string;
  };
};

type AvailableExam = {
  id: string;
  hashtag: string;
  base_url: string;
  state: string;
  starts_at?: string | null;
  ends_at?: string | null;
  completed: boolean;
};

// The student UI is served by the BYOD server itself.  Keeping this relative
// to the current origin allows the same bundle to run in staging and in local
// development without rebuilding Chromium or hard-coding an API origin.
const serviceOrigin = window.location.origin;
const storageToken = 'byod.browser_token';
const storageSession = 'byod.session_id';
const storageExam = 'byod.exam_id';

const title = document.querySelector<HTMLElement>('#title')!;
const message = document.querySelector<HTMLElement>('#message')!;
const entrySection = document.querySelector<HTMLElement>('#entry-section')!;
const entryError = document.querySelector<HTMLElement>('#entry-error')!;
const targetSection = document.querySelector<HTMLElement>('#target-section')!;
const targetElement = document.querySelector<HTMLElement>('#target')!;
const scheduleSection = document.querySelector<HTMLElement>('#schedule-section')!;
const scheduleElement = document.querySelector<HTMLElement>('#schedule')!;
const countdownElement = document.querySelector<HTMLElement>('#countdown')!;
const errorElement = document.querySelector<HTMLElement>('#error')!;
const status = document.querySelector<HTMLElement>('#status')!;
const launch = document.querySelector<HTMLButtonElement>('#launch')!;
const end = document.querySelector<HTMLButtonElement>('#end')!;
const login = document.querySelector<HTMLButtonElement>('#login')!;
const examList = document.querySelector<HTMLElement>('#exam-list')!;

let heartbeatTimer: number | undefined;
let countdownTimer: number | undefined;
let startPollTimer: number | undefined;
let currentConfig: ExamConfig | undefined;
let currentSessionID = '';

type NativeBridgeMessage = {channel: string; name: string; args?: unknown[]};
const embeddedInBrowserShell = window.parent !== window;
let nativeBridgeReady = !embeddedInBrowserShell;
const nativeBridgeQueue: NativeBridgeMessage[] = [];

window.addEventListener('message', (event) => {
  if (!embeddedInBrowserShell || event.source !== window.parent ||
      event.origin !== 'grips://exam.cs.ac.cn')
    return;
  const message = event.data as {channel?: string; type?: string} | null;
  if (message?.channel !== 'byod-browser' || message.type !== 'ready') return;
  nativeBridgeReady = true;
  while (nativeBridgeQueue.length) {
    const queued = nativeBridgeQueue.shift()!;
    window.parent.postMessage(queued, 'grips://exam.cs.ac.cn');
  }
});

function sendNativeMessage(name: string, args?: unknown[]) {
  const message: NativeBridgeMessage = {channel: 'byod-browser', name, args};
  if (!embeddedInBrowserShell) return false;
  if (!nativeBridgeReady) {
    nativeBridgeQueue.push(message);
    return true;
  }
  window.parent.postMessage(message, 'grips://exam.cs.ac.cn');
  return true;
}

class SessionUnauthorizedError extends Error {
  constructor() {
    super('exam session unauthorized');
    this.name = 'SessionUnauthorizedError';
  }
}

function clearNativeExamState() {
  sendNativeMessage('clearByodTunnelConfig');
  sendNativeMessage('setByodFullscreen', [false]);
}

function setNativeFullscreen(required: boolean, locked: boolean) {
  if (!sendNativeMessage('setByodFullscreen', [required, required && locked]) && required)
    throw new Error('BYOD fullscreen control is unavailable');
}

function sourceOrigin(config: ExamConfig): URL {
  const value = config.exam.source_origin;
  if (!value) throw new Error('exam source origin is missing');
  const result = new URL(value);
  if (result.protocol !== 'https:' || !result.hostname || result.username ||
      result.password || result.pathname !== '/' && result.pathname !== '') {
    throw new Error('exam source origin is invalid');
  }
  return result;
}

function sourcePage(config: ExamConfig): URL {
  const value = config.exam.source_url || config.exam.source_origin;
  if (!value) throw new Error('exam source page is missing');
  const result = new URL(value);
  if (result.protocol !== 'https:' || !result.hostname || result.username ||
      result.password || result.hash) {
    throw new Error('exam source page is invalid');
  }
  return result;
}

function endpointAddress(config: ExamConfig): {host: string; port: number} {
  const value = config.tunnel?.endpoint;
  if (!value) throw new Error('exam tunnel endpoint is missing');
  const endpoint = new URL(value.includes('://') ? value : `http://${value}`);
  const port = Number(endpoint.port || 8788);
  if (!endpoint.hostname || !Number.isInteger(port) || port < 1 || port > 65535)
    throw new Error('exam tunnel endpoint is invalid');
  return {host: endpoint.hostname, port};
}

function bytes(text: string): Uint8Array {
  return new TextEncoder().encode(text);
}

async function tunnelProof(ticket: string, endpointID: string,
                           nonce: Uint8Array): Promise<Uint8Array> {
  const key = await crypto.subtle.importKey(
      'raw', bytes(ticket) as unknown as BufferSource,
      {name: 'HMAC', hash: 'SHA-256'}, false, ['sign']);
  const prefix = new Uint8Array(bytes('BYOD').length + 1 + bytes(endpointID).length + nonce.length);
  prefix.set(bytes('BYOD'), 0);
  prefix[4] = 1;
  prefix.set(bytes(endpointID), 5);
  prefix.set(nonce, 5 + bytes(endpointID).length);
  return new Uint8Array(await crypto.subtle.sign(
      'HMAC', key, prefix as unknown as BufferSource));
}

function allowedOrigins(config: ExamConfig): string[] {
  const document = config.policy.document || {};
  const result = new Set<string>(document.allowed_origins || []);
  for (const origin of document.navigation?.allowed_origins || []) result.add(origin);
  return [...result].filter((origin) => /^https?:\/\/[^/?#]+$/.test(origin));
}

async function activateTunnel(config: ExamConfig, sessionID: string,
                              token: string): Promise<void> {
  const path = config.tunnel?.ticket_path?.replace('{session_id}', encodeURIComponent(sessionID)) ||
      `/v1/sessions/${encodeURIComponent(sessionID)}/tunnel-ticket`;
  const response = await fetch(new URL(path, serviceOrigin), {
    method: 'POST', credentials: 'include',
    headers: {'Authorization': `Bearer ${token}`},
  });
  if (response.status === 401) throw new SessionUnauthorizedError();
  if (!response.ok) throw new Error(`exam tunnel ticket rejected (${response.status})`);
  const ticket = await response.json() as {ticket: string; endpoint_id: string};
  if (!ticket.ticket || !ticket.endpoint_id) throw new Error('exam tunnel ticket is incomplete');
  const nonce = new Uint8Array(16);
  crypto.getRandomValues(nonce);
  const proof = await tunnelProof(ticket.ticket, ticket.endpoint_id, nonce);
  const endpoint = endpointAddress(config);
  if (!sendNativeMessage('setByodTunnelConfig', [{
    sourceHost: config.exam.source_host || sourceOrigin(config).hostname,
    proxyHost: endpoint.host,
    proxyPort: endpoint.port,
    endpointId: ticket.endpoint_id,
    ticket: ticket.ticket,
    nonce: [...nonce],
    proof: [...proof],
    allowedOrigins: allowedOrigins(config),
    // The source page replaces this grips:// document, so the browser-side
    // countdown cannot remain responsible for ending the exam. Pass the
    // server's deadline to the native observer, which keeps enforcing the
    // policy after the source navigation and clears it at ends_at.
    examEndsAtMs: config.exam.ends_at ? Date.parse(config.exam.ends_at) : 0,
  }])) throw new Error('BYOD network control is unavailable');
  const browser = config.policy.document?.browser;
  setNativeFullscreen(browser?.require_fullscreen === true,
                      browser?.lock_fullscreen === true);
}

function clearTimers() {
  if (heartbeatTimer !== undefined) window.clearInterval(heartbeatTimer);
  if (countdownTimer !== undefined) window.clearInterval(countdownTimer);
  if (startPollTimer !== undefined) window.clearInterval(startPollTimer);
  heartbeatTimer = undefined;
  countdownTimer = undefined;
  startPollTimer = undefined;
}

function clearLocalSession() {
  localStorage.removeItem(storageToken);
  localStorage.removeItem(storageSession);
  localStorage.removeItem(storageExam);
  currentSessionID = '';
}

function showEntry() {
  clearTimers();
  clearNativeExamState();
  title.textContent = 'Sign in to BYOD exams';
  message.textContent = 'Sign in with Connect to see the exams assigned to you.';
  entrySection.hidden = false;
  targetSection.hidden = true;
  scheduleSection.hidden = true;
  errorElement.hidden = true;
  launch.hidden = true;
  end.hidden = true;
  status.textContent = 'Normal browser mode';
  entryError.hidden = true;
  login.hidden = false;
  login.textContent = 'Sign in with Connect';
  examList.hidden = true;
  examList.replaceChildren();
}

function returnToEntry(messageText = 'The exam session expired. Sign in with Connect again to choose an exam.') {
  clearLocalSession();
  const url = new URL(window.location.href);
  url.search = '';
  window.history.replaceState({}, '', url.toString());
  showEntry();
  entryError.textContent = messageText;
  entryError.hidden = false;
}

function showInvalidLink(text = 'The browser did not start an exam session and remains in normal mode.') {
  clearTimers();
  clearNativeExamState();
  title.textContent = 'Exam unavailable';
  message.textContent = text;
  entrySection.hidden = true;
  targetSection.hidden = true;
  scheduleSection.hidden = true;
  errorElement.hidden = false;
  launch.hidden = true;
  end.hidden = true;
  status.textContent = 'No exam restrictions are active';
  login.hidden = true;
  examList.hidden = true;
}

function showEnded(text = 'The exam is complete. This student cannot enter it again.') {
  clearLocalSession();
  showInvalidLink(text);
  title.textContent = 'Exam completed';
}

function examId(target: URL): string {
  return target.pathname.split('/').filter(Boolean)[0] || '';
}

function configURL(target: URL): URL {
  const url = new URL(target.origin);
  url.pathname = target.pathname.replace(/\/$/, '') + '/.well-known/byod-configuration';
  return url;
}

function returnURI(target?: URL): string {
  if (embeddedInBrowserShell && target) {
    return `grips://exam.cs.ac.cn/?target=${encodeURIComponent(target.href)}`;
  }
  const url = new URL(window.location.href);
  url.searchParams.delete('session_id');
  return url.toString();
}

function formatTime(value?: string | null): string {
  if (!value) return 'not set';
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? 'not set' : date.toLocaleString();
}

function updateCountdown(deadline: string | null | undefined, prefix: string) {
  if (!deadline) {
    countdownElement.textContent = '';
    return;
  }
  const remaining = new Date(deadline).getTime() - Date.now();
  if (remaining <= 0) {
    countdownElement.textContent = prefix === 'Starts in' ? 'Starting…' : 'Time is up';
    return;
  }
  const totalSeconds = Math.floor(remaining / 1000);
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  countdownElement.textContent = `${prefix} ${hours}h ${minutes}m ${seconds}s`;
}

async function completeCurrent(reason = 'manual'): Promise<boolean> {
  const config = currentConfig;
  if (!config || !currentSessionID) return false;
  const token = localStorage.getItem(storageToken) || '';
  try {
    const response = await fetch(new URL(`/v1/exams/${config.exam.id}/complete`, serviceOrigin), {
      method: 'POST',
      credentials: 'include',
      headers: {'Authorization': `Bearer ${token}`, 'Content-Type': 'application/json'},
      body: JSON.stringify({reason}),
    });
    if (response.status === 401) {
      returnToEntry();
      return false;
    }
    if (!response.ok && response.status !== 410) return false;
  } catch {
    return false;
  }
  showEnded(reason === 'manual' ? 'Exam submitted. This student cannot enter it again.' :
      'The exam end time was reached. This student cannot enter it again.');
  return true;
}

async function reportViolation(examOrigin: string, sessionId: string,
                               token: string, type: string) {
  if (!sessionId || !token) return;
  await fetch(new URL(`/v1/sessions/${sessionId}/violations`, examOrigin), {
    method: 'POST', credentials: 'include',
    headers: {'Authorization': `Bearer ${token}`, 'Content-Type': 'application/json'},
    body: JSON.stringify({type}),
  });
}

function showWaiting(target: URL, config: ExamConfig, sessionID: string) {
  title.textContent = 'Exam session authenticated';
  message.textContent = 'Identity verified. The exam will unlock at the scheduled start time.';
  targetElement.textContent = target.href;
  targetSection.hidden = false;
  scheduleSection.hidden = false;
  scheduleElement.textContent = `Starts: ${formatTime(config.exam.starts_at)} · Ends: ${formatTime(config.exam.ends_at)}`;
  entrySection.hidden = true;
  errorElement.hidden = true;
  launch.hidden = true;
  end.hidden = true;
  status.textContent = `Authenticated session ${sessionID.slice(0, 8)}`;
  updateCountdown(config.exam.starts_at, 'Starts in');
  countdownTimer = window.setInterval(() => updateCountdown(config.exam.starts_at, 'Starts in'), 1000);
  startPollTimer = window.setInterval(() => {
    if (!config.exam.starts_at || Date.parse(config.exam.starts_at) <= Date.now()) {
      showConfirm(target, config, sessionID);
    }
  }, 1000);
}

function showConfirm(target: URL, config: ExamConfig, sessionID: string) {
  clearTimers();
  title.textContent = 'Ready to enter the exam';
  message.textContent = 'The exam is open. Confirm to start your attempt and enter the exam.';
  targetElement.textContent = target.href;
  targetSection.hidden = false;
  scheduleSection.hidden = false;
  scheduleElement.textContent = `Ends: ${formatTime(config.exam.ends_at)}`;
  entrySection.hidden = true;
  errorElement.hidden = true;
  launch.hidden = false;
  launch.textContent = 'Confirm and enter exam';
  end.hidden = true;
  status.textContent = `Authenticated session ${sessionID.slice(0, 8)}`;
  currentConfig = config;
  currentSessionID = sessionID;
  launch.onclick = () => void startExam(target, config, sessionID);
}

function showReady(target: URL, config: ExamConfig, sessionID: string) {
  clearTimers();
  title.textContent = 'Exam in progress';
  message.textContent = 'The signed exam policy is active. Submit when you finish.';
  targetElement.textContent = target.href;
  targetSection.hidden = false;
  scheduleSection.hidden = false;
  scheduleElement.textContent = `Ends: ${formatTime(config.exam.ends_at)}`;
  entrySection.hidden = true;
  errorElement.hidden = true;
  launch.hidden = false;
  launch.textContent = 'Open exam';
  end.hidden = false;
  status.textContent = `Active session ${sessionID.slice(0, 8)}`;
  currentConfig = config;
  currentSessionID = sessionID;
  const token = localStorage.getItem(storageToken) || '';
  const configuredHeartbeat = config.policy.document?.session?.heartbeat_seconds ?? 15;
  const heartbeatMs = Math.max(5000, Math.min(300000, configuredHeartbeat * 1000));
  heartbeatTimer = window.setInterval(async () => {
    try {
      const response = await fetch(new URL(`/v1/sessions/${sessionID}/heartbeat`, serviceOrigin), {
        method: 'POST', credentials: 'include',
        headers: {'Authorization': `Bearer ${token}`},
      });
      if (response.status === 410) {
        showEnded('The exam end time was reached. This student cannot enter it again.');
        return;
      }
      if (response.status === 401) {
        returnToEntry();
        return;
      }
      if (!response.ok) throw new Error('heartbeat rejected');
      const state = await response.json();
      if (state.state === 'suspended') {
        showInvalidLink('The exam session was suspended. Contact the proctor.');
      }
    } catch {
      status.textContent = 'Exam service heartbeat failed';
    }
  }, heartbeatMs);
  if (config.exam.ends_at) {
    countdownTimer = window.setInterval(() => {
      updateCountdown(config.exam.ends_at, 'Time remaining');
      if (new Date(config.exam.ends_at as string).getTime() <= Date.now()) {
        void completeCurrent('timeout');
      }
    }, 1000);
    updateCountdown(config.exam.ends_at, 'Time remaining');
  }
  launch.onclick = () => {
    const destination = sourcePage(config);
    if (embeddedInBrowserShell)
      window.top!.location.href = destination.href;
    else
      window.location.href = destination.href;
  };
  end.onclick = () => void completeCurrent('manual');
}

async function startExam(target: URL, config: ExamConfig, sessionID: string) {
  const token = localStorage.getItem(storageToken) || '';
  launch.disabled = true;
  status.textContent = 'Starting exam…';
  try {
    const response = await fetch(new URL(`/v1/sessions/${sessionID}/start`, serviceOrigin), {
      method: 'POST', credentials: 'include',
      headers: {'Authorization': `Bearer ${token}`},
    });
    if (response.status === 401) throw new SessionUnauthorizedError();
    if (response.ok) {
      await activateTunnel(config, sessionID, token);
      showReady(target, config, sessionID);
      return;
    }
    if (response.status === 409) {
      const conflict = await response.json().catch(() => ({})) as {
        error?: string;
        state?: string;
      };
      if (conflict.state === 'active') {
        // Older replicas may return a conflict after another start request
        // already activated the same attempt. Recover without asking the
        // student to authenticate again.
        await activateTunnel(config, sessionID, token);
        showReady(target, config, sessionID);
      } else if (conflict.error === 'exam_not_started') {
        showWaiting(target, config, sessionID);
      } else {
        returnToEntry('The exam session is no longer authenticated. Sign in again.');
      }
      return;
    }
    if (response.status === 410) {
      showEnded('The exam has ended. This student cannot enter it again.');
    }
  } catch (error: unknown) {
    if (error instanceof SessionUnauthorizedError) {
      returnToEntry();
      return;
    }
    const detail = error instanceof Error ? `: ${error.message}` : '';
    status.textContent = `Unable to start the exam${detail}`;
  } finally {
    launch.disabled = false;
  }
}

async function bootstrap(target: URL) {
  const id = examId(target);
  if (!id || target.protocol !== 'https:' || target.hostname !== 'exam.cs.ac.cn' || target.port) {
    showInvalidLink();
    return;
  }
  const configResponse = await fetch(configURL(target), {credentials: 'include'});
  if (configResponse.status === 410) {
    showEnded('This exam has ended.');
    return;
  }
  if (!configResponse.ok) throw new Error('exam configuration fetch failed');
  const config = await configResponse.json() as ExamConfig;
  if (config.exam?.id !== id || config.exam.origin !== target.origin ||
      !config.policy?.signature || !config.policy.key_id ||
      config.policy.document?.exam_id !== id) {
    throw new Error('invalid exam configuration');
  }
  currentConfig = config;
  const pageURL = new URL(window.location.href);
  const existingSession = pageURL.searchParams.get('session_id') ||
      (localStorage.getItem(storageExam) === id ? localStorage.getItem(storageSession) : null);
  const existingToken = localStorage.getItem(storageToken);
  if (!existingSession || !existingToken) {
    const response = await fetch(new URL('/v1/sessions', serviceOrigin), {
      method: 'POST', credentials: 'include', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({exam_id: id, return_uri: returnURI(target)}),
    });
    if (response.status === 410) {
      showEnded('This student has already completed the exam.');
      return;
    }
    if (!response.ok) throw new Error('exam session creation failed');
    const created = await response.json();
    localStorage.setItem(storageSession, created.session_id);
    localStorage.setItem(storageToken, created.browser_token);
    localStorage.setItem(storageExam, id);
    if (embeddedInBrowserShell)
      window.top!.location.href = created.authorization_url;
    else
      window.location.href = created.authorization_url;
    return;
  }
  currentSessionID = existingSession;
  const stateResponse = await fetch(new URL(`/v1/sessions/${existingSession}`, serviceOrigin), {
    headers: {'Authorization': `Bearer ${existingToken}`}, credentials: 'include',
  });
  // A previous OIDC attempt may have left a pending session or an expired
  // browser token in this profile. Do not surface that as an invalid trusted
  // link: discard the stale local session and start a fresh attempt once.
  if (stateResponse.status === 401) {
    clearLocalSession();
    returnToEntry('The previous exam session expired. Sign in with Connect again.');
    return;
  }
  if (stateResponse.status === 410) {
    showEnded('The exam has ended or was already submitted.');
    return;
  }
  if (!stateResponse.ok) throw new Error('exam session lookup failed');
  const state = await stateResponse.json();
  if (state.state === 'active') {
    await activateTunnel(config, existingSession, existingToken);
    showReady(target, config, existingSession);
    return;
  }
  if (state.state !== 'authenticated') {
    returnToEntry('Connect authentication did not complete. Sign in again to choose an exam.');
    return;
  } else {
    // The authenticated landing page never starts the attempt implicitly.
    // The student must explicitly confirm after the schedule is available.
    if (config.exam.starts_at && Date.parse(config.exam.starts_at) > Date.now())
      showWaiting(target, config, existingSession);
    else
      showConfirm(target, config, existingSession);
  }
}

function beginConnectLogin() {
  const destination = embeddedInBrowserShell ?
      'grips://exam.cs.ac.cn/?auth=1' : `${serviceOrigin}/?auth=1`;
  const authorizationURL = `${serviceOrigin}/auth/login?return_to=${encodeURIComponent(destination)}`;
  if (embeddedInBrowserShell)
    window.top!.location.href = authorizationURL;
  else
    window.location.href = authorizationURL;
}

function examLabel(exam: AvailableExam): string {
  if (exam.completed || exam.state === 'ended') return 'Exam ended';
  if (exam.starts_at && Date.parse(exam.starts_at) > Date.now()) return 'Scheduled exam';
  return 'Available exam';
}

function showExamChoices(exams: AvailableExam[]) {
  clearTimers();
  clearNativeExamState();
  title.textContent = 'Choose an exam';
  message.textContent = 'Select one of the exams assigned to your Connect account.';
  entrySection.hidden = false;
  targetSection.hidden = true;
  scheduleSection.hidden = true;
  errorElement.hidden = true;
  launch.hidden = true;
  end.hidden = true;
  login.hidden = true;
  examList.hidden = false;
  examList.replaceChildren();
  if (!exams.length) {
    entryError.textContent = 'No exams are currently assigned to this account.';
    entryError.hidden = false;
    status.textContent = 'Signed in; no exam assignments';
    return;
  }
  entryError.hidden = true;
  for (const exam of exams) {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'exam-choice';
    button.disabled = exam.completed || exam.state === 'ended';
    const titleNode = document.createElement('strong');
    titleNode.textContent = `#${exam.hashtag}`;
    const detail = document.createElement('small');
    detail.textContent = `${examLabel(exam)} · starts ${formatTime(exam.starts_at)} · ends ${formatTime(exam.ends_at)}`;
    button.append(titleNode, detail);
    button.onclick = () => void selectExam(exam);
    examList.append(button);
  }
  status.textContent = 'Signed in';
}

async function loadAvailableExams() {
  entryError.hidden = true;
  login.disabled = true;
  try {
    const me = await fetch(new URL('/auth/me', serviceOrigin), {credentials: 'include'});
    if (me.status === 401) {
      showEntry();
      entryError.textContent = 'Sign in with Connect to view your exams.';
      entryError.hidden = false;
      return;
    }
    if (!me.ok) throw new Error('account lookup failed');
    const response = await fetch(new URL('/v1/exams/available', serviceOrigin), {credentials: 'include'});
    if (response.status === 401) {
      showEntry();
      entryError.textContent = 'Your Connect login has expired. Sign in again.';
      entryError.hidden = false;
      return;
    }
    if (!response.ok) throw new Error('exam list failed');
    showExamChoices(await response.json() as AvailableExam[]);
  } catch {
    entryError.textContent = 'Unable to reach the exam service.';
    entryError.hidden = false;
  } finally {
    login.disabled = false;
  }
}

async function selectExam(exam: AvailableExam) {
  const target = new URL(`${serviceOrigin}/${encodeURIComponent(exam.id)}`);
  const url = new URL(window.location.href);
  url.search = '';
  url.searchParams.set('target', target.href);
  window.history.replaceState({}, '', url.toString());
  login.disabled = true;
  entryError.hidden = true;
  try {
    const response = await fetch(new URL('/v1/sessions', serviceOrigin), {
      method: 'POST', credentials: 'include', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({exam_id: exam.id, return_uri: embeddedInBrowserShell ?
        `grips://exam.cs.ac.cn/?target=${encodeURIComponent(target.href)}` : url.toString()}),
    });
    if (response.status === 401) {
      showEntry();
      entryError.textContent = 'Your Connect login has expired. Sign in again.';
      entryError.hidden = false;
      return;
    }
    if (response.status === 410) {
      showEnded('This exam has ended or was already submitted.');
      return;
    }
    if (response.status === 403) {
      showExamChoices([]);
      entryError.textContent = 'You are not allowed to enter this exam.';
      entryError.hidden = false;
      return;
    }
    if (!response.ok) throw new Error('exam session creation failed');
    const created = await response.json() as {session_id: string; browser_token: string; state: string; authorization_url?: string};
    localStorage.setItem(storageSession, created.session_id);
    localStorage.setItem(storageToken, created.browser_token);
    localStorage.setItem(storageExam, exam.id);
    url.searchParams.set('session_id', created.session_id);
    window.history.replaceState({}, '', url.toString());
    if (created.authorization_url) {
      if (embeddedInBrowserShell)
        window.top!.location.href = created.authorization_url;
      else
        window.location.href = created.authorization_url;
      return;
    }
    await bootstrap(target);
  } catch (error: unknown) {
    if (error instanceof SessionUnauthorizedError) {
      returnToEntry();
      return;
    }
    entryError.textContent = 'Unable to start the exam session.';
    entryError.hidden = false;
  } finally {
    login.disabled = false;
  }
}

login.onclick = () => beginConnectLogin();

// The HTTPS shell must be embedded by the browser-owned Grips document. If a
// stale OIDC return URI or a bookmarked HTTPS URL lands here top-level, native
// tunnel/fullscreen controls are unavailable; return to the shell before
// creating or starting a session.
const directHTTPSExamShell = !embeddedInBrowserShell &&
    window.location.protocol === 'https:' &&
    window.location.hostname === 'exam.cs.ac.cn' &&
    !window.location.port;
if (directHTTPSExamShell) {
  showInvalidLink('Open grips://exam.cs.ac.cn in BYOD Browser to start or resume this exam.');
} else {
  const params = new URLSearchParams(window.location.search);
  const targetValue = params.get('target');
  const action = params.get('action');
  const violation = params.get('violation');
  if (action === 'complete') {
  const actionExamID = params.get('exam_id') || localStorage.getItem(storageExam) || '';
  currentSessionID = localStorage.getItem(storageSession) || '';
  if (actionExamID && currentSessionID) {
    currentConfig = {exam: {id: actionExamID, origin: serviceOrigin, proxy_origin: serviceOrigin, unlock_path: ''}, oidc: {authorization_endpoint: ''}, policy: {alg: '', key_id: '', document: {}, signature: ''}};
    void completeCurrent('manual');
  } else {
    showEnded('There is no active exam session to submit.');
  }
  } else if (violation === 'background') {
  const sessionID = localStorage.getItem(storageSession) || '';
  const token = localStorage.getItem(storageToken) || '';
  if (targetValue) {
    try {
      const target = new URL(targetValue);
      void reportViolation(target.origin, sessionID, token, 'background');
    } catch { /* keep the browser in the safe error state */ }
  }
  showInvalidLink('The exam was suspended because the browser moved to the background. Contact the proctor.');
  } else if (params.has('ended')) {
  showEnded('The exam was submitted. This student cannot enter it again.');
  } else if (params.get('auth') === '1' && !targetValue) {
  void loadAvailableExams();
  } else if (!targetValue || params.has('error')) {
  void loadAvailableExams();
  } else {
  try {
    void bootstrap(new URL(targetValue)).catch((error: unknown) => {
      if (error instanceof SessionUnauthorizedError) {
        returnToEntry();
      } else {
        showInvalidLink(String(error));
      }
    });
  } catch {
    showInvalidLink();
  }
  }
}

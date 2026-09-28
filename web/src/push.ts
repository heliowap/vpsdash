import { api, ApiError } from './api'

export type PushState =
  | 'checking'
  | 'unsupported'
  | 'install-required'
  | 'no-worker'
  | 'unconfigured'
  | 'denied'
  | 'off'
  | 'on'

export function pushSupported() {
  return window.isSecureContext && 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window
}

// iOS delivers Web Push only to web apps added to the home screen.
export function needsHomeScreenInstall() {
  const standalone = window.matchMedia('(display-mode: standalone)').matches || (navigator as Navigator & { standalone?: boolean }).standalone === true
  return /iPhone|iPad|iPod/.test(navigator.userAgent) && !standalone
}

function decodeKey(value: string) {
  const base64 = (value + '='.repeat((4 - value.length % 4) % 4)).replace(/-/g, '+').replace(/_/g, '/')
  return Uint8Array.from(atob(base64), char => char.charCodeAt(0))
}

function sameKey(buffer: ArrayBuffer | null | undefined, key: Uint8Array) {
  if (!buffer) return false
  const bytes = new Uint8Array(buffer)
  return bytes.length === key.length && bytes.every((value, index) => value === key[index])
}

// The worker registers on page load (production builds only), so wait for
// it briefly instead of reporting it missing during the first visit.
async function registration(): Promise<ServiceWorkerRegistration | null> {
  return Promise.race([
    navigator.serviceWorker.ready,
    new Promise<null>(resolve => window.setTimeout(() => resolve(null), 8_000))
  ])
}

export type PushSnapshot = { state: PushState; endpoint?: string }

// readPushState reports this device's state and re-registers an existing
// browser subscription with the server, replacing it if the VAPID key changed.
export async function readPushState(csrf: string): Promise<PushSnapshot> {
  if (!pushSupported()) return { state: needsHomeScreenInstall() ? 'install-required' : 'unsupported' }
  const server = await api.pushStatus()
  if (!server.configured) return { state: 'unconfigured' }
  if (Notification.permission === 'denied') return { state: 'denied' }
  const reg = await registration()
  if (!reg) return { state: 'no-worker' }
  const current = await reg.pushManager.getSubscription()
  if (!current) return { state: 'off' }
  const key = decodeKey(server.public_key)
  if (!sameKey(current.options.applicationServerKey, key)) {
    await current.unsubscribe()
    if (Notification.permission !== 'granted') return { state: 'off' }
    return enablePush(csrf, server.public_key)
  }
  await api.pushSubscribe(current.toJSON(), csrf)
  return { state: 'on', endpoint: current.endpoint }
}

export async function enablePush(csrf: string, publicKey?: string): Promise<PushSnapshot> {
  const key = publicKey ?? (await api.pushStatus()).public_key
  if (!key) return { state: 'unconfigured' }
  const permission = await Notification.requestPermission()
  if (permission === 'denied') return { state: 'denied' }
  if (permission !== 'granted') return { state: 'off' }
  const reg = await registration()
  if (!reg) return { state: 'no-worker' }
  let subscription: PushSubscription
  try {
    subscription = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: decodeKey(key) })
  } catch (err) {
    // Chromium refuses push in private windows with NotAllowedError, and
    // reports an unreachable push service with AbortError.
    const name = err instanceof DOMException ? err.name : ''
    throw new Error(name === 'NotAllowedError'
      ? 'O navegador recusou a inscrição. Janelas anônimas não recebem notificações push.'
      : 'O serviço de push do navegador não respondeu. Tente novamente em instantes.')
  }
  try {
    await api.pushSubscribe(subscription.toJSON(), csrf)
  } catch (err) {
    await subscription.unsubscribe().catch(() => undefined)
    if (err instanceof ApiError && err.status === 503) return { state: 'unconfigured' }
    throw err
  }
  return { state: 'on', endpoint: subscription.endpoint }
}

// forgetLocalSubscription drops a subscription the server no longer accepts,
// so the next activation asks the push service for a fresh one.
export async function forgetLocalSubscription() {
  const reg = await registration()
  const subscription = await reg?.pushManager.getSubscription()
  await subscription?.unsubscribe()
}

export async function disablePush(csrf: string): Promise<PushSnapshot> {
  const reg = await registration()
  const subscription = await reg?.pushManager.getSubscription()
  if (subscription) {
    await api.pushUnsubscribe(subscription.endpoint, csrf)
    await subscription.unsubscribe()
  }
  return { state: 'off' }
}

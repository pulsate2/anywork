// LightRemote Service Worker —— Web Push 通知处理。
// 纯 ES2017,无模块语法(public/ 文件原样复制,不做转译)。
'use strict'

self.addEventListener('install', () => {
  // 立即激活,让新版本 SW 马上接管。
  self.skipWaiting()
})

self.addEventListener('activate', (event) => {
  event.waitUntil(self.clients.claim())
})

// 收到推送 → 显示系统通知。
self.addEventListener('push', (event) => {
  let data = { title: 'LightRemote', body: '通知', url: '/' }
  try {
    if (event.data) {
      data = Object.assign(data, event.data.json())
    }
  } catch (e) { /* 非 JSON 负载,用默认值 */ }

  const options = {
    body: data.body || '',
    icon: '/favicon.svg',
    badge: '/favicon.svg',
    tag: data.tag || 'lightremote',
    renotify: true,
    data: { url: data.url || '/' },
    requireInteraction: false,
  }
  event.waitUntil(self.registration.showNotification(data.title || 'LightRemote', options))
})

// 点击通知 → 聚焦已打开的窗口,否则新开。
self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  const target = (event.notification.data && event.notification.data.url) || '/'
  event.waitUntil(
    self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((list) => {
      for (const client of list) {
        if ('focus' in client) return client.focus()
      }
      return self.clients.openWindow(target)
    })
  )
})

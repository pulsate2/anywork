const assert = require('node:assert/strict')
const { readFileSync } = require('node:fs')
const path = require('node:path')
const { test } = require('node:test')
const vm = require('node:vm')
const ts = require('typescript')

function setup() {
  class MockWebSocket {
    static OPEN = 1
    static instances = []
    readyState = 1
    sent = []
    constructor() { MockWebSocket.instances.push(this) }
    send(data) { this.sent.push(JSON.parse(data)) }
    close() { this.readyState = 3 }
    receive(data) { this.onmessage({ data }) }
  }
  const source = readFileSync(path.join(__dirname, '../src/api/term.ts'), 'utf8')
  const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS } }).outputText
  const context = {
    exports: {}, WebSocket: MockWebSocket, location: { protocol: 'http:', host: 'localhost' },
    ArrayBuffer, DataView, Uint8Array, setTimeout, clearTimeout,
  }
  vm.runInNewContext(compiled, context)
  const events = []
  const client = new context.exports.TermClient((event) => events.push(event))
  function connect() {
    const pending = client.connect()
    const socket = MockWebSocket.instances.at(-1)
    socket.onopen()
    return { socket, pending }
  }
  const { socket } = connect()
  return { client, socket, events, connect }
}

function output(requestId, text) {
  const data = new TextEncoder().encode(text)
  const frame = new Uint8Array(5 + data.length)
  frame[0] = 'o'.charCodeAt(0)
  new DataView(frame.buffer).setUint32(1, requestId)
  frame.set(data, 5)
  return frame.buffer
}

function ack(socket, id, requestId) {
  socket.receive('s' + JSON.stringify({ type: 'session', id, requestId }))
}

test('switching ignores queued old output and late attach acknowledgements', () => {
  const { client, socket, events } = setup()
  client.attach('old')
  const oldRequest = socket.sent.at(-1).requestId
  client.attach('new')
  const newRequest = socket.sent.at(-1).requestId
  socket.receive(output(oldRequest, 'OLD'))
  ack(socket, 'old', oldRequest)
  socket.receive(output(newRequest, 'NEW'))
  ack(socket, 'new', newRequest)
  assert.equal(client.currentSession, 'new')
  assert.deepEqual(events.filter((e) => e.type === 'output').map((e) => new TextDecoder().decode(e.data)), ['NEW'])
  assert.deepEqual(events.filter((e) => e.type === 'session').map((e) => e.session.id), ['new'])
})

test('re-attaching to the same session distinguishes old output from its replay', () => {
  const { client, socket, events } = setup()
  client.attach('same')
  const oldRequest = socket.sent.at(-1).requestId
  client.attach('same')
  const newRequest = socket.sent.at(-1).requestId
  socket.receive(output(oldRequest, 'stale'))
  socket.receive(output(newRequest, 'history'))
  assert.deepEqual(events.filter((e) => e.type === 'output').map((e) => new TextDecoder().decode(e.data)), ['history'])
})

test('creating accepts new replay before acknowledgement and rejects the old session', () => {
  const { client, socket, events } = setup()
  client.attach('old')
  const oldRequest = socket.sent.at(-1).requestId
  client.createSession('/workspace', '', 80, 24)
  const requestId = socket.sent.at(-1).requestId
  client.input('should not reach old session')
  assert.equal(socket.sent.at(-1).type, 'create')
  socket.receive(output(oldRequest, 'OLD'))
  socket.receive(output(requestId, 'PROMPT'))
  ack(socket, 'created', requestId)
  client.input('hello')
  assert.equal(socket.sent.at(-1).sid, 'created')
  assert.deepEqual(events.filter((e) => e.type === 'output').map((e) => new TextDecoder().decode(e.data)), ['PROMPT'])
})

test('detach rejects pending output and session responses', () => {
  const { client, socket, events } = setup()
  client.attach('old')
  const requestId = socket.sent.at(-1).requestId
  client.detach()
  socket.receive(output(requestId, 'OLD'))
  ack(socket, 'old', requestId)
  assert.equal(client.currentSession, null)
  assert.equal(events.filter((e) => e.type === 'output' || e.type === 'session').length, 0)
})

test('reconnecting ignores events from the replaced socket', () => {
  const { client, socket: oldSocket, events, connect } = setup()
  client.attach('old')
  const { socket } = connect()
  client.attach('new')
  const requestId = socket.sent.at(-1).requestId
  oldSocket.receive(output(requestId, 'OLD_SOCKET'))
  ack(oldSocket, 'wrong', requestId)
  socket.receive(output(requestId, 'NEW_SOCKET'))
  ack(socket, 'new', requestId)
  assert.equal(client.currentSession, 'new')
  assert.deepEqual(events.filter((e) => e.type === 'output').map((e) => new TextDecoder().decode(e.data)), ['NEW_SOCKET'])
})

test('xterm reset barrier clears pending old writes before new output is parsed', async () => {
  global.self = global
  const { Terminal } = require('@xterm/xterm')
  const terminal = new Terminal({ cols: 80, rows: 24 })
  try {
    terminal.write('OLD_OUTPUT')
    terminal.reset()
    terminal.write('', () => terminal.reset())
    await new Promise((resolve) => terminal.write('NEW_OUTPUT', resolve))
    assert.equal(terminal.buffer.active.getLine(0).translateToString(true), 'NEW_OUTPUT')
  } finally {
    terminal.dispose()
  }
})

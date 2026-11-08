type MockApp = Partial<Record<keyof AppApi, any>>
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { flushPromises } from '@vue/test-utils'

const handlers: Record<string, (p: any) => void> = {}
vi.mock('../wailsjs/runtime/runtime', () => ({ EventsOn: (n: string, f: (p: any) => void) => { handlers[n] = f; return () => {} }, EventsOff: () => {} }))

import {
  signInItems, startSignIn, cancelSignIn, signInNotNow, startSignInTracking, promptFor, _resetSignInForTests,
} from './useSignIn'
import { connectionList, openPromptFor, connect, signInFor, _resetConnectionsForTests } from './useConnections'

const conn = (over: Partial<ConnectionInfo>): ConnectionInfo => ({
  name: 'store', type: 's3', scope: 'local', readOnly: true, default: false, status: 'needs-credentials', remote: true, catalogless: true,
  signIn: 'aws-sso:dev', signInNeeded: { kind: 'aws-sso', subject: 'dev', reason: 'expired' }, ...over,
})
const PROMPT: SignInPrompt = { connection: 'store', profile: 'dev', url: 'https://device.sso.us-east-1.amazonaws.com/?user_code=ABCD-EFGH', code: 'ABCD-EFGH', expiresAt: '2026-10-16T12:10:00Z' }

function makeApp(over: MockApp = {}) {
  const app: MockApp = {
    ListConnections: vi.fn(async () => ({ connections: connectionList.value, relaunchRequired: false })),
    StartSignIn: vi.fn(async () => PROMPT),
    CancelSignIn: vi.fn(async () => {}),
    ...over,
  }
  ;(globalThis as any).window = (globalThis as any).window || {}
  ;(window as any).go = { main: { App: app } }
  ;(window as any).runtime = {}
  return app
}

describe('useSignIn', () => {
  beforeEach(() => {
    _resetSignInForTests(); _resetConnectionsForTests()
    for (const k of Object.keys(handlers)) delete handlers[k]
  })

  it('lists one item per AWS profile that needs a sign-in, and nothing for other statuses', () => {
    makeApp()
    connectionList.value = [
      conn({ name: 'store' }),
      conn({ name: 'store2', signInNeeded: { kind: 'aws-sso', subject: 'dev', reason: 'expired' } }),
      conn({ name: 'ok', status: 'ok', signInNeeded: undefined }),
      conn({ name: 'pg', type: 'postgres', signIn: 'keychain', signInNeeded: undefined }),
      conn({ name: 'prod', signIn: 'aws-sso:prod', signInNeeded: { kind: 'aws-sso', subject: 'prod', reason: 'missing' } }),
    ]
    expect(signInItems.value.map((i) => [i.profile, i.names, i.reason])).toEqual([['dev', ['store', 'store2'], 'expired'], ['prod', ['prod'], 'missing']])
  })

  it('Sign in starts the flow once and keeps the prompt; a second click does not start another', async () => {
    const app = makeApp()
    connectionList.value = [conn({})]
    await startSignIn('store')
    await startSignIn('store')
    expect(app.StartSignIn).toHaveBeenCalledTimes(1)
    expect(app.StartSignIn).toHaveBeenCalledWith({ name: 'store', scope: '' })
    expect(promptFor('dev')).toEqual(PROMPT)
    expect(signInItems.value[0].prompt).toEqual(PROMPT)
  })

  it('a failed start shows the error on the item', async () => {
    makeApp({ StartSignIn: vi.fn(async () => { throw new Error("couldn't reach AWS sign-in") }) })
    connectionList.value = [conn({})]
    await startSignIn('store')
    expect(signInItems.value[0].error).toBe("couldn't reach AWS sign-in")
    expect(promptFor('dev')).toBeNull()
  })

  it('Not now hides the item for this open only; a running flow or an error shows it again', async () => {
    makeApp()
    startSignInTracking()
    connectionList.value = [conn({})]
    signInNotNow('dev')
    expect(signInItems.value).toEqual([])
    handlers['workspace:opened']({})
    expect(signInItems.value).toHaveLength(1)
    signInNotNow('dev')
    await startSignIn('store')
    expect(signInItems.value).toHaveLength(1)
  })

  it('signin:done clears the prompt and refreshes; signin:failed keeps the sentence', async () => {
    const app = makeApp()
    startSignInTracking()
    connectionList.value = [conn({})]
    await startSignIn('store')
    handlers['signin:failed']({ name: 'store', profile: 'dev', message: 'the AWS sign-in request was denied' })
    await flushPromises()
    expect(promptFor('dev')).toBeNull()
    expect(signInItems.value[0].error).toBe('the AWS sign-in request was denied')
    expect(app.ListConnections).toHaveBeenCalled()
    await startSignIn('store')
    expect(signInItems.value[0].error).toBe('')
    connectionList.value = []
    handlers['signin:done']({ name: 'store', profile: 'dev' })
    await flushPromises()
    expect(promptFor('dev')).toBeNull()
    expect(signInItems.value).toEqual([])
  })

  it('Cancel stops the flow by the connection it was started from', async () => {
    const app = makeApp()
    connectionList.value = [conn({})]
    await startSignIn('store')
    await cancelSignIn('dev')
    expect(app.CancelSignIn).toHaveBeenCalledWith({ name: 'store', scope: '' })
    expect(promptFor('dev')).toBeNull()
  })

  it('only an explicit Sign in starts the flow: a prompt raised by a failed Run, a selector pick, a tree expand or a deep link just reveals the banner', async () => {
    const app = makeApp({ ConnectConnection: vi.fn(async () => { throw new Error('needs sign-in') }) })
    startSignInTracking()
    const c = conn({})
    connectionList.value = [c]
    signInNotNow('dev')
    expect(signInItems.value).toEqual([])
    // useQueryEditor / ConnectionSelect / the tree / the table deep link all end in openPromptFor or connect().
    expect(openPromptFor(c)).toBe(true)
    await connect('store')
    await flushPromises()
    expect(app.StartSignIn).not.toHaveBeenCalled()
    expect(signInFor.value).toBeNull()
    expect(signInItems.value).toHaveLength(1)
    expect(signInItems.value[0].prompt).toBeNull()
    // Any other needs-credentials connection still gets the password prompt.
    expect(openPromptFor(conn({ name: 'pg', type: 'postgres', signIn: 'keychain', signInNeeded: undefined }))).toBe(true)
    expect(signInFor.value).toBe('pg')
  })

  it('a catalog waiting on its storage connection sends the prompt and the banner to that connection, never to itself', async () => {
    const app = makeApp()
    startSignInTracking()
    const store = conn({ name: 'aws_lake' })
    const cat = conn({ name: 's3t', type: 'iceberg_catalog', catalogless: false, signIn: undefined, signInNeeded: undefined, blockedBy: 'aws_lake', detail: 'needs the "aws_lake" connection: it needs credentials' })
    connectionList.value = [store, cat]
    // the banner lists the storage connection's profile once, with the storage connection only
    expect(signInItems.value.map((i) => [i.profile, i.names])).toEqual([['dev', ['aws_lake']]])
    // Add it / Fix / a failed Run / a tree expand on the catalog reveal the storage connection's banner
    expect(openPromptFor(cat)).toBe(true)
    expect(signInFor.value).toBeNull()
    expect(app.StartSignIn).not.toHaveBeenCalled()
    // a password-style blocker gets its own prompt, not the catalog's
    connectionList.value = [conn({ name: 'pg', type: 'postgres', signIn: 'keychain', signInNeeded: undefined, catalogless: false }), { ...cat, blockedBy: 'pg' }]
    expect(openPromptFor(connectionList.value[1])).toBe(true)
    expect(signInFor.value).toBe('pg')
    // a blocker that is gone, or a loop, opens nothing
    expect(openPromptFor({ ...cat, blockedBy: 'nope' })).toBe(false)
    connectionList.value = [{ ...cat, name: 'a', blockedBy: 'b' }, { ...cat, name: 'b', blockedBy: 'a' }]
    expect(openPromptFor(connectionList.value[0])).toBe(false)
  })

  it('a server change opens the prompt that links to the editor, without navigating away or starting a sign-in', () => {
    const app = makeApp()
    startSignInTracking()
    const c = conn({ serverChanged: { old: 's3.a', new: 's3.b' } })
    connectionList.value = [c]
    expect(openPromptFor(c)).toBe(true)
    window.location.hash = '#/queries'
    expect(openPromptFor(c)).toBe(true)
    expect(window.location.hash).toBe('#/queries')
    expect(app.StartSignIn).not.toHaveBeenCalled()
    expect(signInFor.value).toBe('store')
  })

  it('an answer that arrives after the workspace changed is dropped', async () => {
    let release: (p: SignInPrompt) => void = () => {}
    makeApp({ StartSignIn: vi.fn(() => new Promise<SignInPrompt>((res) => { release = res })) })
    startSignInTracking()
    connectionList.value = [conn({})]
    const p = startSignIn('store')
    handlers['workspace:opened']({})
    release(PROMPT)
    await p
    expect(promptFor('dev')).toBeNull()
  })

  it('workspace close forgets everything', async () => {
    makeApp()
    startSignInTracking()
    connectionList.value = [conn({})]
    await startSignIn('store')
    handlers['workspace:closed']({})
    expect(promptFor('dev')).toBeNull()
  })
})

// Display helpers for connections (#117, SPEC 05): the type tile text, the scope
// words, the status shapes and the one-line "what to do" under a row. The UI
// never shows the file-level scope words (shared / local / user).

const TYPE_CODE: Record<string, string> = {
  duckdb: 'DK', postgres: 'PG', motherduck: 'MD', sqlite: 'SQ', s3: 'S3', gcs: 'GC', r2: 'R2', mysql: 'MY', ducklake: 'DL', azure: 'AZ', iceberg: 'IC', iceberg_catalog: 'IR', delta: 'DT',
}
const TYPE_LABEL: Record<string, string> = {
  duckdb: 'DuckDB file', postgres: 'Postgres', motherduck: 'MotherDuck', sqlite: 'SQLite file',
  s3: 'Amazon S3', gcs: 'Google Cloud Storage', r2: 'Cloudflare R2', mysql: 'MySQL', ducklake: 'DuckLake',
  azure: 'Azure Blob / Data Lake', iceberg: 'Iceberg table', iceberg_catalog: 'Iceberg catalog', delta: 'Delta Lake table',
}
export const typeCode = (t: string) => TYPE_CODE[t] ?? t.slice(0, 2).toUpperCase()
export const typeLabel = (t: string) => TYPE_LABEL[t] ?? t

export const SCOPE_LABEL: Record<ConnScope, string> = { user: 'All my workspaces', shared: 'Team', local: 'This computer' }
export const SCOPE_TIP: Record<ConnScope, string> = {
  user: 'Saved for you, in every workspace',
  shared: 'Saved in slaw.yaml, shared with the team through git',
  local: 'Saved on this computer only, for this workspace',
}
export const scopeLabel = (s: string) => SCOPE_LABEL[s as ConnScope] ?? s

type StatusKey = 'off' | 'ok' | 'err' | 'creds' | 'ext' | 'lock'

export const STATUS_META: Record<StatusKey, { icon: string; label: string }> = {
  off: { icon: 'circle', label: 'Not connected' },
  ok: { icon: 'circleDot', label: 'Connected' },
  err: { icon: 'circleAlert', label: 'Can’t connect' },
  creds: { icon: 'key', label: 'Needs credentials' },
  ext: { icon: 'download', label: 'Extension needed' },
  lock: { icon: 'lock', label: 'In use by another program' },
}

export function statusKey(status: string): StatusKey {
  switch (status) {
    case 'ok': return 'ok'
    case 'error': return 'err'
    case 'needs-credentials': return 'creds'
    case 'extension-missing': return 'ext'
    case 'locked-by': return 'lock'
    default: return 'off'
  }
}

type RowAction = 'fix' | 'signin' | 'install' | 'takeover'

export interface DetailLine {
  text: string
  action: RowAction
  actionLabel: string
}

// firstSentence keeps a redacted error to one short line for the rail.
export function oneLine(s: string | undefined, max = 90): string {
  const t = (s ?? '').replace(/\s+/g, ' ').trim()
  if (!t) return 'something went wrong'
  const cut = t.search(/[.\n]( |$)/)
  const line = cut > 0 ? t.slice(0, cut) : t
  return line.length > max ? line.slice(0, max - 1) + '…' : line
}

const NEEDS: Record<string, string> = {
  motherduck: 'a MotherDuck token',
  postgres: 'a username and password',
  s3: 'access keys',
  gcs: 'access keys',
  r2: 'access keys',
  mysql: 'a username and password',
  azure: 'an account key, connection string or service principal',
}

// ssoProfileOf is the AWS profile a connection signs in with, or '' when it
// does not use one: from the status hint, else from the person's sign-in method.
export function ssoProfileOf(c: ConnectionInfo): string {
  if (c.signInNeeded?.kind === 'aws-sso') return c.signInNeeded.subject
  return (c.signIn ?? '').startsWith('aws-sso:') ? c.signIn!.slice(8) : ''
}

// detailLine is the text under a row, only when the person can act on it.
export function detailLine(c: ConnectionInfo): DetailLine | null {
  switch (statusKey(c.status)) {
    case 'err':
      return { text: `Can’t connect: ${oneLine(c.detail)}.`, action: 'fix', actionLabel: 'Fix' }
    case 'creds':
      if (c.serverChanged) {
        return { text: `The server changed since you signed in (${c.serverChanged.old} to ${c.serverChanged.new}).`, action: 'signin', actionLabel: 'Sign in again' }
      }
      if (ssoProfileOf(c)) {
        const p = ssoProfileOf(c)
        const gone = c.signInNeeded?.reason === 'expired'
        return { text: gone ? `AWS session for profile ${p} has expired.` : `Not signed in to AWS profile ${p}.`, action: 'signin', actionLabel: 'Sign in' }
      }
      return { text: `Needs ${NEEDS[c.type] ?? 'credentials'}.`, action: 'signin', actionLabel: 'Add it' }
    case 'ext':
      return { text: `Needs the ${c.extension || 'database'} extension.`, action: 'install', actionLabel: 'Install…' }
    case 'lock':
      return { text: c.lockedByPid ? `In use by another program (pid ${c.lockedByPid}).` : 'In use by another program.', action: 'takeover', actionLabel: 'Take over' }
    default:
      return null
  }
}

// catalogInStorage is true for a DuckLake whose catalog is a storage URL: the
// catalog is read from object storage and can only be read, so "turn off
// Read-only" is not an option for it (the definition is refused when writable).
export function catalogInStorage(def: { type: string; path?: string } | null | undefined): boolean {
  return !!def && def.type === 'ducklake' && /^[a-z][a-z0-9+.-]*:\/\//i.test(def.path ?? '')
}

// statusLabel is the text a screen reader hears for a row's status.
export function statusLabel(c: ConnectionInfo): string {
  return STATUS_META[statusKey(c.status)].label
}

// quoteIdent quotes one SQL identifier part.
const quoteIdent = (s: string) => `"${s.replace(/"/g, '""')}"`

// selectFrom is the starter statement for "New query on this table".
export function selectFrom(qualified: string): string {
  return `SELECT * FROM ${qualified.split('.').map(quoteIdent).join('.')} LIMIT 100`
}

// Connection names in SQL: lowercase letters, digits, underscore (the catalog alias rule).
export const NAME_RE = /^[a-z_][a-z0-9_]{0,62}$/

// The request key for a type's credential field name (ConnTypeSpec.credentials
// name -> CredentialInput), and the suffix an environment sign-in appends to its
// prefix (SLAW_PASSWORD). One map for the editor form and the sign-in prompt.
export const CRED_KEY: Record<string, keyof CredentialInput> = { user: 'username', key_id: 'keyId', password: 'password', token: 'token', secret: 'secret' }
export const CRED_ENV_SUFFIX: Record<string, string> = { user: 'USER', key_id: 'KEY_ID', password: 'PASSWORD', token: 'TOKEN', secret: 'SECRET' }

// fieldVisible says whether a type's field applies to the definition being
// edited: every visibleWhen condition names an option and the values it must
// have, and a blank value counts as that option's default (so an unset "auth"
// is "oauth2"). A field with no conditions is always visible.
export function fieldVisible(fd: Pick<ConnField, 'visibleWhen'>, fields: ConnField[], options: Record<string, string>): boolean {
  for (const c of fd.visibleWhen ?? []) {
    let v = (options[c.field] ?? '').trim()
    if (v === '') v = fields.find((x) => x.name === c.field)?.default ?? ''
    if (!c.in.includes(v)) return false
  }
  return true
}

// connectionChoices lists the names of the workspace's connections of a type, for
// a field of kind 'connection' (an s3_tables catalog is signed by an s3 one).
export function connectionChoices(list: Pick<ConnectionInfo, 'name' | 'type'>[], type: string | undefined): string[] {
  return list.filter((c) => !type || c.type === type).map((c) => c.name).sort()
}

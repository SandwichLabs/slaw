/// <reference types="vite/client" />

declare module '*.vue' {
  import type { DefineComponent } from 'vue'
  const component: DefineComponent<{}, {}, any>
  export default component
}

// The typed shape of the Wails-bound Go App (every exported method on
// studio.App). Hand-written; kept honest by two checks: scripts/check-bindings.sh
// (every Go method is declared here and has a caller) and
// studio/frontend/scripts/check-arity.mjs (CI build-smoke: parameter counts match
// the wails-generated App.d.ts). The ONLY runtime access to window.go is
// src/lib/bindings.ts.
interface AppApi {
  // About (#40)
  GetBuildInfo(): Promise<BuildInfo>
  GetThirdPartyNotices(): Promise<string>
  GetPreferences(): Promise<{ theme: string; systemLight: string; systemDark: string; reduceMotion: boolean; uiScale: number }>
  SetPreferences(p: { theme: string; systemLight: string; systemDark: string; reduceMotion: boolean; uiScale: number }): Promise<void>
  TakePreferencesNotice(): Promise<string>
  ListUserThemes(): Promise<{ themes: { path: string; filename: string; json: string }[] | null; problems: string[] | null; dir: string }>
  OpenThemesDir(): Promise<string>
  // Updates (Settings > About > Updates, the first-run prompt)
  GetUpdateState(): Promise<UpdateState>
  SetUpdateCheck(mode: string): Promise<void>
  CheckForUpdate(): Promise<UpdateResult>
  AutoCheckForUpdate(): Promise<UpdateResult | null>
  SkipUpdate(tag: string): Promise<void>
  ApplyUpdate(tag: string): Promise<UpdateApplyResult>
  RestartAfterUpdate(): Promise<void>
  // Workspaces (#111)
  GetInitialWorkspace(): Promise<{ path: string; message: string; bundlePath: string }>
  MarkLaunchHandled(): Promise<void>
  ListRecentWorkspaces(): Promise<RecentWorkspace[]>
  RemoveRecentWorkspace(path: string): Promise<RecentWorkspace | null>
  RestoreRecentWorkspace(r: RecentWorkspace): Promise<void>
  RelocateRecentWorkspace(oldPath: string, newPath: string): Promise<void>
  ChooseFolder(title: string): Promise<string>
  SelectWorkspaceFolder(): Promise<string>
  CheckGitAvailable(): Promise<boolean>
  CheckWorkspaceDir(parentDir: string, name: string): Promise<WorkspaceDirCheck>
  OpenWorkspace(path: string): Promise<OpenWorkspaceResult>
  CreateWorkspace(req: CreateWorkspaceRequest): Promise<OpenWorkspaceResult>
  // .slaw bundles (#118)
  GetBundleContent(): Promise<BundleContent>
  SelectBundleFile(): Promise<string>
  ExportBundle(req: BundleExportRequest): Promise<BundleExportResult>
  PreviewBundleImport(path: string, passphrase: string): Promise<BundlePreview>
  CommitBundleImport(handle: string, d: BundleDecisions): Promise<BundleResult>
  ImportBundleIntoNewWorkspace(handle: string, req: CreateWorkspaceRequest, d: BundleDecisions): Promise<{ opened: OpenWorkspaceResult; result: BundleResult }>
  DiscardBundleImport(handle: string): Promise<void>
  CloseWorkspace(): Promise<void>
  TakeOverWorkspace(): Promise<OpenWorkspaceResult>
  GetWorkspaceInfo(): Promise<WorkspaceInfo>
  GetSessionID(): Promise<string>
  IsWorkspaceOpen(): Promise<boolean>
  SetWorkspaceSettings(s: WorkspaceSettings): Promise<WorkspaceInfo>
  RegenerateWorkspaceID(): Promise<WorkspaceInfo>
  TrustLocalSettings(): Promise<WorkspaceInfo>
  ResolveConflict(req: ConflictRequest): Promise<ConflictResult>
  ListTemplates(): Promise<TemplateInfo[]>
  ListCatalogGroups(): Promise<CatalogGroup[]>
  // Connections (#117)
  ListConnectionTypes(): Promise<ConnTypeSpec[]>
  FindStorageFor(req: FindStorageRequest): Promise<StorageNeedsView>
  ListConnections(): Promise<ConnectionList>
  GetConnection(name: string): Promise<ConnectionDetail>
  GetConnectionInScope(ref: ConnectionRef): Promise<ConnectionDetail>
  ConnectionFingerprint(def: ConnectionDef): Promise<string>
  SaveConnection(req: SaveConnectionRequest): Promise<ConnectionDetail>
  MoveConnection(req: { name: string; to: ConnScope }): Promise<ConnectionDetail>
  DeleteConnection(ref: ConnectionRef): Promise<void>
  TestConnection(req: TestConnectionRequest): Promise<TestResultView>
  ListMotherDuckDatabases(req: ListMotherDuckDatabasesRequest): Promise<MotherDuckDatabasesView>
  ConnectConnection(ref: ConnectionRef): Promise<ConnectionInfo | null>
  ConnectWithCredentials(req: ConnectWithCredentialsRequest): Promise<ConnectionInfo | null>
  DisconnectConnection(ref: ConnectionRef): Promise<ConnectionInfo | null>
  StartSignIn(ref: ConnectionRef): Promise<SignInPrompt>
  CancelSignIn(ref: ConnectionRef): Promise<void>
  RefreshCatalog(ref: ConnectionRef): Promise<void>
  SetConnectionCredentials(req: SetCredentialsRequest): Promise<ConnectionInfo | null>
  ClearConnectionCredentials(req: { name: string; field: string }): Promise<ConnectionInfo | null>
  GetConnectionCredentialStatus(name: string): Promise<ConnCredentialStatus>
  ListCatalog(req: { connection: string; schema: string; table: string }): Promise<CatalogListing>
  GetTableCapabilities(table: string): Promise<TableCapabilities>
  InstallExtension(req: { name: string }): Promise<void>
  ListExtensions(): Promise<ExtensionInfo[]>
  StartMCP(): Promise<MCPStatus>
  StopMCP(): Promise<void>
  GetMCPStatus(): Promise<MCPStatus>
  // The workspace's .env (#168): names and state only, never values
  GetDotEnvStatus(): Promise<DotEnvStatus>
  AllowDotEnv(): Promise<DotEnvStatus>
  DenyDotEnv(): Promise<DotEnvStatus>
  RevokeDotEnv(): Promise<DotEnvStatus>
  // Receipt settings (Settings > Workspace > Receipts): this person's retention, usage and clear
  GetReceiptSettings(): Promise<ReceiptSettingsInfo>
  SetReceiptSettings(s: ReceiptSettings): Promise<void>
  ClearReceipts(): Promise<void>
  // Evidence (the drawer; `slaw receipts show|list`): what a receipt says, never a result value
  GetReceipt(id: string): Promise<ReceiptRecord>
  ListReceipts(filter: ReceiptFilter): Promise<ReceiptListing>
  GetClaudeConfig(): Promise<{ name: string; desktop: string; code: string }>
  CancelQuit(): Promise<void>
  HoldQuit(): Promise<void>
  Quit(): Promise<void>
  ListSkills(): Promise<SkillSummary[]>
  SaveSkill(name: string, description: string, instructions: string, tableName: string, rev: string): Promise<void>
  DeleteSkill(name: string): Promise<void>
  // Semantic definitions (E4): what the Semantics view lists and opens
  ListDefinitions(): Promise<DefinitionList>
  GetDefinition(kind: string, name: string): Promise<DefinitionDetail>
  // Proposed semantic definitions (E4): the person's side of the inbox
  ListProposals(all: boolean): Promise<Proposal[]>
  GetProposal(id: string): Promise<ProposalPreview>
  AcceptProposal(id: string, shownRev: string, overwrite: boolean): Promise<void>
  AcceptProposals(pins: { id: string; shown_rev: string }[]): Promise<BulkAcceptResult>
  RejectProposal(id: string): Promise<void>
  // Running what a definition declares (E4): assertions, join profiling, a metric's "Try it"
  RunAssertions(kind: string, name: string): Promise<AssertionRow[]>
  ProfileJoin(name: string): Promise<JoinProfile>
  RunMetric(name: string, dims: string[]): Promise<QueryResult>
  // Watches (E7): standing questions with a condition, checked when the person asks
  ListWatches(): Promise<WatchList>
  GetWatch(name: string): Promise<WatchDetail>
  WatchHistory(name: string, limit: number): Promise<WatchEntry[]>
  CheckWatches(names: string[]): Promise<WatchCheckRun>
  PreviewWatch(draft: WatchDraft): Promise<string>
  SaveWatch(draft: WatchDraft): Promise<string>
  DeleteWatch(name: string, rev: string): Promise<void>
  ListTables(): Promise<string[]>
  DescribeTable(name: string): Promise<TableInfo>
  GetTableData(name: string, page: number, pageSize: number, sortCol: string, sortDir: string): Promise<TableData>
  GetTableDataFiltered(name: string, spec: FilterSpec, page: number, pageSize: number, sortCol: string, sortDir: string): Promise<TableData>
  SaveFilterAsQuery(name: string, spec: FilterSpec, columns: string[], title: string): Promise<string>
  InsertRow(table: string, values: Record<string, any>): Promise<WriteResult>
  UpdateRow(table: string, pkCol: string, pkVal: any, values: Record<string, any>): Promise<WriteResult>
  DeleteRow(table: string, pkCol: string, pkVal: any): Promise<WriteResult>
  CreateTable(name: string, columns: ColumnInfo[]): Promise<void>
  DropTable(name: string): Promise<void>
  RunQuery(req: { sql: string; connection: string; tabId: string; queryId?: string; params?: Record<string, string | number | boolean> }): Promise<QueryResult>
  CompleteSQL(req: { sql: string; offset: number; connection: string }): Promise<CompleteSQLResult>
  ReplOpen(connection: string): Promise<ReplOpenResult>
  ReplInput(id: string, data: string): Promise<void>
  ReplResize(id: string, cols: number, rows: number): Promise<void>
  ReplAck(id: string, seq: number): Promise<void>
  ReplClose(id: string): Promise<void>
  ReplUse(id: string, name: string): Promise<string>
  RunA2UIQuery(sql: string): Promise<A2UIQueryResult>
  RunA2UIQueryIn(sql: string, connection: string, source: RenderSource): Promise<A2UIQueryResult>
  ListArtifacts(): Promise<ArtifactSummary[]>
  GetArtifact(name: string): Promise<ArtifactDetail>
  RunArtifactQuery(name: string, query: string, values: Record<string, unknown>): Promise<A2UIQueryResult>
  GetArtifactSource(name: string): Promise<ArtifactSource>
  ArtifactViewState(name: string): Promise<ArtifactViewStatus>
  AllowArtifactView(name: string, hash: string): Promise<ArtifactViewStatus>
  DenyArtifactView(name: string): Promise<ArtifactViewStatus>
  RevokeArtifactView(name: string): Promise<ArtifactViewStatus>
  ListArtifactViewDecisions(): Promise<ArtifactViewDecision[]>
  OpenArtifactView(name: string, origin: string): Promise<ArtifactViewGrant>
  OpenArtifactPage(name: string, origin: string): Promise<ArtifactPageGrant>
  PageWindowSupport(): Promise<string>
  OpenArtifactPageWindow(name: string, query: string, theme: Record<string, string>): Promise<PageWindowInfo>
  ClosePageWindow(name: string): Promise<void>
  SetPageAsk(name: string, on: boolean): Promise<void>
  ListAgentActivity(): Promise<AgentActivityEntry[]>
  RunArtifactViewQuery(token: string, query: string, values: Record<string, unknown>): Promise<A2UIQueryResult>
  ValidateArtifactViewPage(name: string, page: string): Promise<string>
  SaveArtifact(name: string, manifest: string, view: string, rev: string): Promise<ArtifactSaveResult>
  CheckArtifact(name: string): Promise<string[]>
  NewArtifact(name: string, kind: string): Promise<ArtifactSource>
  DeleteArtifact(name: string, rev: string): Promise<void>
  ListQueries(): Promise<QuerySummary[]>
  GetQuery(id: string): Promise<QueryDetail>
  SaveQueryOn(req: { id: string; title: string; sql: string; description: string; rev: string; connection: string }): Promise<{ id: string; rev: string }>
  DeleteQuery(id: string): Promise<void>
  GetDashboard(): Promise<DashboardData>
  GetAgentConfig(): Promise<AgentConfig>
  ProbeKeychain(): Promise<KeychainProbe>
  GetLLMKeyStatus(provider: string): Promise<LLMKeyStatus>
  GetLLMSettings(): Promise<LLMSettingsView>
  SetLLMSettings(s: LLMSettingsInput): Promise<void>
  ListLLMModels(provider: string, endpoint: string): Promise<LLMModelList>
  SetLLMKey(provider: string, key: string): Promise<void>
  ClearLLMKey(provider: string): Promise<void>
  MigrateLLMKeyToKeychain(overwrite: boolean): Promise<void>
  ResolveAgentApproval(id: string, approved: boolean): Promise<void>
  SelectImportFile(): Promise<string>
  InspectImport(src: ImportSource): Promise<ImportInspection>
  ImportFile(o: ImportOptions): Promise<ImportSummary>
  // Agent chat and task mode (#63, #66)
  AgentChatStream(turnId: string, message: string, pinnedContext: string, history: { role: string; text: string }[]): Promise<AgentChatResult>
  AgentTask(req: { taskId: string; mode: string; description: string; cell?: import('./composables/useAiCellDialog').TaskCellArg; followUp?: { role: string; text: string }[]; tables?: string[]; notebookSummary?: string[] }): Promise<import('./composables/useAiCellDialog').TaskEnvelope>
  CancelAgentTask(taskId: string): Promise<void>
  CancelAgentQuery(turnId: string): Promise<void>
  CancelRun(key: string): Promise<void>
  // Notebooks (#66)
  ListNotebooks(): Promise<WailsNotebookSummary[]>
  GetNotebook(id: string): Promise<WailsNotebook>
  CreateNotebook(title: string): Promise<WailsNotebook>
  DeleteNotebook(id: string): Promise<void>
  SaveNotebook(nb: Record<string, any>, sessionId: string): Promise<{ notebook: WailsNotebook; warnings: string[] | null }>
  SelectNotebookFile(): Promise<string>
  ImportNotebook(path: string): Promise<{ notebook: WailsNotebook; warnings: string[] | null }>
  ExportNotebook(id: string, dst: string): Promise<string>
  SetActiveNotebook(id: string): Promise<void>
  // Exports
  ExportQueryResults(sql: string, format: string, dst: string): Promise<string>
}

// A notebook as the bindings return it: the editor owns the cell shapes, so
// only the envelope is typed here.
interface WailsNotebook {
  id: string
  title: string
  rev?: string
  cells: any[]
  [key: string]: any
}

interface WailsNotebookSummary {
  id: string
  title: string
  cellCount: number
  updatedAt: string
  error?: string
}

interface AgentChatResult {
  text: string
  steps: { tool: string; input: string; output: string; isError?: boolean }[]
}

// WriteResult is returned by the transactional CRUD methods
// (InsertRow/UpdateRow/DeleteRow) and reports the number of affected rows.
interface WriteResult {
  affected: number
}

// WriteError is the structured, field-keyed CRUD failure surfaced by the
// backend so the form UI can highlight the offending controls. It arrives as a
// rejected promise (JSON-encoded) rather than the resolved value.
interface WriteError {
  message: string
  fields?: Record<string, string>
}

// TemplateInfo mirrors studio.TemplateInfo: a bundled starter-template
// descriptor surfaced to the New workspace dialog's "Start from" cards.
interface TemplateInfo {
  name: string
  description: string
}

// --- Workspaces (#111): mirrors studio/workspace.go ---

interface RecentWorkspace {
  path: string
  id: string
  name: string
  lastOpened: string
  gitBranch: string
  missing: boolean
}

type ConnScope = 'shared' | 'local' | 'user'

interface ConnectionInfo {
  name: string
  type: string
  scope: ConnScope
  shadows?: string[]
  readOnly: boolean
  default: boolean
  status: string
  detail?: string
  lockedByPid?: number
  // A shared lake or table definition: table metadata decides which files DuckDB reads.
  warning?: string
  remote: boolean
  catalogless?: boolean
  signIn?: string
  extension?: string
  serverChanged?: { old: string; new: string }
  // Set on needs-credentials that is because an AWS profile must sign in
  // (#131): the profile and why. No account, role or start URL.
  signInNeeded?: SignInHint
  // Set on needs-credentials of a connection that waits on another one (an s3_tables
  // catalog and its storage connection): the sign-in to do is that connection's.
  blockedBy?: string
}
// What the window may know about an AWS sign-in: the profile and the reason.
interface SignInHint { kind: string; subject: string; reason: string }
// What the person is shown to finish a sign-in: the page that was opened and the code it should show.
interface SignInPrompt { connection: string; profile: string; url: string; code: string; expiresAt: string }

// --- Connections (#117): mirrors studio/connections.go. No field here is a
// secret; the password, token and secret key are write-only. ---

interface ConnField {
  name: string
  label: string
  kind: 'text' | 'number' | 'enum' | 'path' | 'url' | 'connection'
  help?: string
  default?: string
  choices?: string[]
  // The words shown for a choice; one without a label is shown as stored.
  choiceLabels?: Record<string, string>
  required: boolean
  secret: boolean
  perPerson: boolean
  option?: boolean
  prominent?: boolean
  // kind 'connection': the picker lists the workspace's connections of this type.
  connectionType?: string
  // Shown only while every condition holds (a blank value counts as the field's default).
  visibleWhen?: { field: string; in: string[] }[]
}
interface ConnTypeSpec {
  id: string
  label: string
  description: string
  fields: ConnField[]
  credentials: ConnField[]
  requiresExtension: string[]
  capabilities: { catalog: boolean; writable: boolean; readOnlyAttach: boolean; remote: boolean; test: boolean; subprocess?: boolean }
  fingerprintFields: string[]
  signInMethods: string[]
  // S3-compatible store presets (s3 only; an empty list for every other type).
  presets?: ConnPreset[]
  // The type's metadata decides which files DuckDB reads (Go: workspace.MetadataDrivenType); warning is Go's MetadataWarning.
  metadataDriven?: boolean
  warning?: string
  soon: boolean
}
interface ConnPreset {
  id: string
  label: string
  // Only endpoint, url_style and use_ssl; "<region>" in an endpoint stands for the Region field.
  options: Record<string, string>
  regionHint?: string
  help?: string
}
// Storage detection (#130): which connection covers a URL typed into a lake or table form.
interface FindStorageRequest { urls: string[] }
interface StorageMatchView { name: string; type: string; scope: string; in: ConnScope; status: string }
interface StorageSuggestionView { type: string; scope?: string; bucket_scope?: string; name: string }
interface StorageNeedView {
  url: string
  match?: StorageMatchView
  suggest?: StorageSuggestionView
  problem?: string
  group?: number
  templated?: boolean
}
interface StorageNeedsView { needs: StorageNeedView[] }
interface ConnectionList {
  connections: ConnectionInfo[]
  relaunchRequired: boolean
  relaunchMessage?: string
}
interface ConnectionRef { name: string; scope: ConnScope | '' }
interface ConnectionDef {
  name: string
  type: string
  path?: string
  host?: string
  port?: number
  database?: string
  region?: string
  scope?: string
  read_only?: boolean | null
  credentials?: string
  options?: Record<string, string>
}
interface ConnCredentialStatus {
  method: string
  username?: string
  keyId?: string
  secretSet: boolean
  sessionOnly?: boolean
  fingerprintMatches: boolean
  available: boolean
  detail?: string
  env?: { name: string; set: boolean }[]
  // aws-sso only: the profile, whether its session works, and when it ends.
  profile?: string
  signedIn?: boolean
  expiresAt?: string
}
interface ConnectionDetail {
  name: string
  scope: ConnScope
  def: ConnectionDef
  fingerprint: string
  shadows: string[]
  shadowedBy?: string
  signIn: string
  signInHint?: string
  credential: ConnCredentialStatus
  info: ConnectionInfo
}
interface SaveConnectionRequest { scope: ConnScope; def: ConnectionDef; isNew: boolean; signIn: string; confirmRebind?: boolean }
interface CredentialInput { username: string; keyId: string; password: string; token: string; secret: string }
interface SetCredentialsRequest extends CredentialInput { name: string; method: string; remember: boolean; confirmRebind?: boolean }
interface ConnectWithCredentialsRequest extends CredentialInput { name: string; remember: boolean }
interface TestConnectionRequest { scope: ConnScope; def: ConnectionDef; signIn: string; credentials: CredentialInput }
interface ListMotherDuckDatabasesRequest { name: string; scope: ConnScope; signIn: string; credentials: CredentialInput; install: boolean }
interface MotherDuckDatabase { name: string; share: boolean }
interface MotherDuckDatabasesView { ok: boolean; status: string; databases: MotherDuckDatabase[]; source?: string; message?: string; installExtension?: string }
interface TestResultView {
  ok: boolean
  status: string
  ms: number
  count: number
  detail?: string
  summary?: string
  error?: { code: string; message: string; details: string }
  installExtension?: string
  requirement?: string
}
interface CatalogNode { kind: 'schema' | 'table' | 'view' | 'column'; name: string; qualified?: string; rows: number; dataType?: string; pk?: boolean; fk?: boolean }
interface CatalogListing { kind: 'schemas' | 'tables' | 'columns'; schema: string; flattened: boolean; nodes: CatalogNode[]; more?: number }
interface TableCapabilities { connection: string; editable: boolean; reason: string; text: string }
interface ExtensionInfo { name: string; purpose: string; neededBy: string[]; installed: boolean }

interface AgentPolicyInfo {
  sandbox: string
  askBeforeWrites: boolean
}

interface HolderInfo {
  app: string
  pid: number
  servesMcp: boolean
  text: string
}

interface WorkspaceInfo {
  root: string
  id: string
  name: string
  sessionId: string
  gitBranch: string
  isGit: boolean
  defaultConnection: string
  connections: ConnectionInfo[]
  counts: { notebooks: number; queries: number; skills: number; tables: number }
  policy: AgentPolicyInfo
  team: AgentPolicyInfo
  local: AgentPolicyInfo
  teamSandboxSet: boolean
  readOnly: boolean
  holder?: HolderInfo
  issues: { connection: string; message: string }[]
  warnings: string[]
  duplicateIds: string[]
  localUntrusted: boolean
  mcpRunning: boolean
}

interface OpenWorkspaceResult {
  status: 'opened' | 'not-workspace'
  path: string
  info?: WorkspaceInfo
  warnings?: string[]
}

interface CreateWorkspaceRequest {
  name: string
  dir: string
  parentDir: string
  git: boolean
  template: string
  inPlace: boolean
}

interface WorkspaceDirCheck { dir: string; exists: boolean; notEmpty: boolean }

interface WorkspaceSettings {
  name: string
  defaultConnection: string
  teamSandbox: string
  teamAskBeforeChanges: boolean
  sandbox: string
  askBeforeChanges: boolean
  loadedTeamSandbox?: string
  loadedTeamAsk?: boolean
}

interface ConflictRequest {
  kind: 'notebook' | 'query' | 'skill' | 'artifact'
  id: string
  action: 'reload' | 'overwrite' | 'saveCopy'
  data: Record<string, any>
  rev?: string
  connection?: string
}
interface ConflictResult { id: string; rev: string; title: string }

// ArtifactViewStatus mirrors workspace.ArtifactViewStatus (#159): where a custom
// (script) view stands for this person, with the exact files its hash covers.
interface ArtifactViewStatus {
  name: string
  kind: 'a2ui' | 'script' | 'html' | ''
  state: 'none' | 'undecided' | 'allowed' | 'changed' | 'denied'
  hash: string
  manifest: string
  source: string
  /** An HTML page's derived host; its files (text ones with their text), the queries it uses and the capabilities it declares. */
  page_kind?: 'static' | 'scripted'
  queries?: string[]
  capabilities?: string[]
  /** The queries the page can run, with the connection each runs on ("" is the workspace database): every manifest query for a scripted page (script can run any), the templates' for a static one. */
  query_uses?: { name: string; connection?: string; used?: boolean }[]
  /** The metrics the artifact's queries call (#282); their files and saved queries are part of the hash. */
  metrics_used?: ArtifactMetricUse[]
  metrics_note?: string
  /** Allowed, but only a metric or saved query it reads changed, not its own files. */
  deps_changed?: boolean
  /** An HTML page's allow-prompt text, read with the hash it describes (never a second read). */
  disclosure?: PageDisclosure
  files?: ArtifactViewFile[]
  error?: string
}
// PageDisclosure mirrors studio.PageDisclosure: the plain-language text of an HTML page's allow prompt.
interface PageDisclosure {
  host: string
  capabilities: { name: string; text: string }[]
  network: string
  credential: string
  askProvider: string
}
interface ArtifactMetricUse { name: string; query?: string; measure?: string; dimensions?: string[]; missing?: boolean }
interface ArtifactViewFile { path: string; role: string; size: number; text?: string }
// ArtifactPageGrant mirrors studio.ArtifactPageGrant: the token a static page's frame
// URL is made from (/slaw-view/<token>/page), bound to one PageHash.
interface ArtifactPageGrant { token: string; hash: string }
// AgentActivityEntry mirrors studio.ActivityEntry: one use of the model by a page (names and sizes, never text).
interface AgentActivityEntry { at: string; source: string; name: string; kind: string; promptBytes: number; replyBytes: number; outcome: string }
// PageWindowInfo mirrors studio.PageWindowInfo: a scripted page's window, opened for one PageHash.
interface PageWindowInfo { name: string; hash: string }
// ArtifactViewGrant mirrors studio.ArtifactViewGrant (#160): the token an allowed
// view's Worker URL is made from (/slaw-view/<token>/worker.js), bound to one hash.
interface ArtifactViewGrant { token: string; hash: string }
interface ArtifactViewDecision {
  name: string
  path: string
  decision: 'allow' | 'deny'
  state: 'allowed' | 'changed' | 'denied' | 'gone' | 'undecided'
  declaresAsk?: boolean
  askOn?: boolean
  kind?: 'script' | 'html'
  pageKind?: 'static' | 'scripted'
  capabilities?: string[]
  metricsUsed?: ArtifactMetricUse[]
  depsChanged?: boolean
}

interface DotEnvSatisfies { connection: string; signIn: string; variables: string[] }
interface DotEnvStatus {
  state: 'none' | 'undecided' | 'allowed' | 'changed' | 'denied'
  path: string
  names: string[]
  warnings?: string[]
  ignored?: string
  trackedByGit: boolean
  loaded: boolean
  satisfies: DotEnvSatisfies[]
}

interface ReceiptSettings { keepParams: boolean; days: number; maxCount: number; maxMB: number }
interface ReceiptUsage { count: number; bytes: number; oldest: string }
interface ReceiptSettingsInfo {
  settings: ReceiptSettings
  effective: ReceiptSettings
  defaults: ReceiptSettings
  floors: ReceiptSettings
  ceilings: ReceiptSettings
  usage: ReceiptUsage
  available: boolean
}

interface MCPStatus { running: boolean; url: string; canStart: boolean; reason: string; startError: string; remembered: boolean; saveError: string }

interface CatalogTable { name: string; qualified: string; kind: 'table' | 'view' }
interface CatalogGroup {
  connection: string
  type: string
  status: string
  detail?: string
  readOnly: boolean
  default: boolean
  tables: CatalogTable[]
}

// SkillSummary mirrors studio.SkillSummary (#62): one Skill as the Skills
// view lists/renders it — built-in, user-authored, or import-generated.
interface SkillSummary {
  name: string
  description: string
  instructions: string
  table_name?: string
  source: string // 'built-in' | 'user' | 'import'
  rev?: string // store revision as listed; sent back on save
}

// Proposal mirrors studio.Proposal (internal/semantics/proposals): a definition
// an agent proposed, waiting for the person. The definition itself is read
// through GetProposal's proposed_text.
interface Proposal {
  id: string
  kind: 'entity' | 'metric' | 'join' | 'glossary'
  name: string
  status: 'open' | 'accepted' | 'rejected' | ''
  proposed_at: string
  proposed_by: string
  rationale: string
  evidence: string[]
  replaces_rev: string
  decided_at: string
  rev?: string
  error?: string // an unreadable proposal file
}

// BulkAcceptResult mirrors studio.BulkAcceptResult: what "Accept all" wrote, in
// order, and the proposal that stopped it (failed_id) with the reason (error).
interface BulkAcceptResult {
  accepted: { id: string; kind: Proposal['kind']; name: string; target: string }[]
  failed_id?: string
  error?: string
  written_not_marked?: boolean // the stopping proposal's file was written but the proposal could not be marked accepted
}

// ProposalPreview mirrors studio.ProposalPreview: a proposal beside the file it
// would replace. proposed_text is the whole file after accepting; the diff is
// drawn client-side.
interface ProposalPreview {
  proposal: Proposal
  target: string
  exists: boolean
  current_text: string
  current_rev: string
  proposed_text: string
  stale: boolean
  stale_reason?: string
  shown_rev: string // pins the version on screen; sent back on accept
  warnings: string[] // a missing saved query, a dropped measured block, lost comments
}

// DefinitionSummary / DefinitionList mirror workspace.DefinitionList: one line per
// entity, metric and join (a file that cannot be read lists with its error), the
// glossary terms and the number of open proposals.
interface DefinitionSummary {
  kind: 'entity' | 'metric' | 'join'
  name: string
  title?: string
  summary?: string
  rev?: string
  error?: string
}
interface DefinitionList {
  entities: DefinitionSummary[]
  metrics: DefinitionSummary[]
  joins: DefinitionSummary[]
  terms: string[]
  glossary_rev?: string
  glossary_error?: string
  open_proposals: number
}
interface SemanticAssertion { kind: string; column?: string; values?: string[]; min?: number; max?: number; max_age?: string; min_rows?: number }
interface SemanticEntity {
  name: string
  description?: string
  grain: string
  table: string
  connection?: string
  id_column: string
  columns?: { name: string; pii?: boolean; description?: string }[]
  owner?: string
  assertions?: SemanticAssertion[]
}
interface SemanticMetric {
  name: string
  title?: string
  description?: string
  query: string
  measure: string
  dimensions?: string[]
  format?: string
  owner?: string
  settled_by?: { text: string; ref?: string }
  assertions?: SemanticAssertion[]
}
interface SemanticJoin {
  name: string
  left: string
  right: string
  on: { left: string; right: string }[]
  cardinality: string
  description?: string
  measured?: JoinProfile['measured']
}
// DefinitionDetail mirrors workspace.DefinitionDetail: one definition opened, its file
// text as stored and, when the file is valid, the decoded document of its kind.
interface DefinitionDetail {
  kind: 'entity' | 'metric' | 'join' | 'glossary'
  name: string
  text: string
  rev: string
  error?: string
  entity?: SemanticEntity
  metric?: SemanticMetric
  join?: SemanticJoin
  glossary?: { text: string; terms: string[] }
  query_missing?: boolean
}

// AssertionRow mirrors studio.AssertionRow (internal/semantics/assert.Row): one
// assertion's outcome. 'error' means the check could not run, which says nothing
// about the data.
interface AssertionRow {
  subject_kind: 'entity' | 'metric'
  subject: string
  assertion: string
  column?: string
  status: 'pass' | 'fail' | 'error'
  violations: number
  detail?: string // what a freshness or row_count check saw, e.g. 'latest 2026-09-30'
  duration_ms: number
  error?: string
  receipt?: string // reserved for receipts; empty for now
}

// JoinProfile mirrors studio.JoinProfile (internal/semantics.Profile): what a
// profiling run measured, and where it contradicts the declared cardinality.
interface JoinProfile {
  join: string
  declared: string
  measured: {
    at: string
    left_rows: number
    right_rows: number
    max_fanout: number
    left_orphans: number
    right_orphans: number
    verdict: 'consistent' | 'fan_out' | 'orphans'
  }
  left_max_dup: number
  warnings: string[]
  rev: string
}

// WatchWhen mirrors studio.WatchWhen: a watch's condition. Thresholds are the text
// of the file, so a large integer is never rounded by a JavaScript number.
interface WatchWhen { rows?: 'any' | 'none' | ''; above?: string; below?: string; outside?: string[] }
interface WatchQuestion { query?: string; params?: Record<string, string>; metric?: string }
// WatchDraft mirrors studio.WatchDraft: the New watch / Edit form's answer. rev is
// the revision the form loaded ('' creates); overwrite is "Keep mine".
interface WatchDraft { name: string; description?: string; question: WatchQuestion; when: WatchWhen; rev?: string; overwrite?: boolean }
// WatchEntry mirrors watches.Entry: one recorded check (never the rows it read).
interface WatchEntry {
  watch: string; rev?: string; at: string; status: 'ok' | 'alert' | 'error'
  value?: number | string; rows?: number; rows_capped?: boolean; reason: string; duration_ms: number
}
// WatchSummary mirrors studio.WatchSummary: one row of the list.
interface WatchSummary {
  name: string; description?: string; question: WatchQuestion; when: WatchWhen
  question_text?: string; when_text?: string; rev?: string
  error?: string // a file that could not be read
  last?: WatchEntry; last_checked?: string; last_fired?: string; last_status?: '' | 'ok' | 'alert' | 'error'
}
interface WatchList { watches: WatchSummary[]; alerts: number; history_error?: string }
interface WatchDetail extends WatchSummary { text: string }
// WatchSample mirrors workspace.WatchSample: the first rows of an alerting rows
// check. It exists in the result of the check only; nothing stores it.
interface WatchSample { columns: string[]; rows: string[][]; capped?: boolean }
// WatchResult mirrors workspace.WatchResult: a check's outcome plus the sample.
interface WatchResult extends WatchEntry { question: string; condition: string; sample?: WatchSample; history_error?: string; interrupted?: boolean }
interface WatchCheckRun { results: WatchResult[]; cancelled?: boolean }

interface PageSummary {
  id: string
  title: string
  page_type: string
  sort_order: number
  migrated_to: string
}

interface PageDetail {
  id: string
  title: string
  content: string
  page_type: string
  sort_order: number
}

interface ColumnInfo {
  name: string
  type: string
  not_null: boolean
  pk: boolean
  // fk is read-only output from DescribeTable (a real DuckDB FOREIGN KEY
  // constraint, read from duckdb_constraints() — #68 dropped the
  // _vibe_relationships metadata table); CreateTable/AddColumn ignore it.
  fk?: boolean
}

interface TableInfo {
  name: string
  columns: ColumnInfo[]
}

// FilterCondition / FilterSpec mirror studio.FilterCondition / studio.FilterSpec
// (the SQL-free datasheet visual-querying unit behind Filter-by-Selection and
// Filter-by-Form, plan §2.3 / P2.5).
interface FilterCondition {
  column: string
  op: string
  value?: any
}

interface FilterSpec {
  combinator: string
  conditions: FilterCondition[]
}

interface TableData {
  columns: string[]
  types: string[]
  rows: any[][]
  page: number
  page_size: number
  total_rows: number
  total_pages: number
}

interface CompletionItem {
  label: string
  kind: 'keyword' | 'connection' | 'schema' | 'table' | 'view' | 'column' | 'function' | 'alias' | 'metric'
  detail?: string
  insert?: string
}

interface CompleteSQLResult {
  from: number
  items: CompletionItem[]
  stale?: string[]
  building?: boolean
  incomplete?: boolean
}

// ReplOpenResult: a new Data Console session. Its output arrives as
// "repl:out:<id>" events carrying ReplFrame.
interface ReplOpenResult {
  id: string
  cols: number
  rows: number
  connection: string
}

interface ReplFrame {
  seq: number
  data: string
  closed?: boolean
  reason?: string
}

interface QueryResult {
  columns: ColumnInfo[]
  rows: any[][]
  duration: string
  row_count: number
  total_rows: number
  truncated?: boolean // stopped at the cap without counting the rest (total_rows is then only row_count)
  receipt_id?: string // the run's receipt (docs/plans/run-receipts.md); absent when none was kept
  receipt_note?: ReceiptNote // why there is none when one was due
}

// KeychainProbe / LLMKeyStatus (#114): where the agent's API key comes from.
// There is no way to read the key itself; the bindings set, clear and report.
interface KeychainProbe {
  available: boolean
  backend: string
  detail?: string
}

interface LLMKeyStatus {
  provider: string
  source: 'env' | 'keychain' | 'file' | 'none'
  set: boolean
  fileKey: boolean
  canMigrate: boolean
  keychainAvailable: boolean
  keychainHasKey: boolean
  detail?: string
}

// Settings → App → "The agent's model" (app-global; no workspace needed).
interface LLMProviderInfo {
  id: string
  label: string
  description: string
  needsKey: boolean
  keyOptional: boolean
  keyLabel: string
  keyEnvVars: string[]
  hasEndpoint: boolean
  defaultEndpoint?: string
  hasAccount: boolean
  defaultModel: string
  suggestedModels: string[]
}

interface LLMSettingsInput {
  provider: string
  model: string
  endpoint: string
  accountId: string
  gateway: string
}

interface LLMSettingsView extends LLMSettingsInput {
  effectiveModel: string
  defaultModel: string
  accountIdFromEnv: boolean
  envOverrides: Record<string, string>
  providers: LLMProviderInfo[]
}

interface LLMModelList {
  models: string[] | null
  error?: string
  unreachable?: boolean
}

interface AgentConfig {
  provider: string
  model: string
  endpoint: string
  sandbox: string
  askBeforeWrites: boolean
  teamSandbox?: string
  teamAskBeforeWrites?: boolean
}

interface QuerySummary {
  id: string
  title: string
  engine: string
}

// QueryParamT mirrors queries.Param: a saved query's declared $name parameter
// (type is text, number, integer, date or boolean; default is a string).
interface QueryParamT {
  name: string
  type: 'text' | 'number' | 'integer' | 'date' | 'boolean'
  required: boolean
  default?: string
}

interface QueryDetail {
  id: string
  title: string
  sql: string
  engine: string
  parameters: QueryParamT[]
  created_at: string
  updated_at: string
  rev?: string
}

// ArtifactCounts mirrors studio.ArtifactCounts: artifacts by Library kind; a
// broken artifact counts once, under broken.
interface ArtifactCounts {
  live: number
  page: number
  script: number
  broken: number
}

interface DashboardData {
  title: string
  description: string
  notebook_count: number
  table_count: number
  query_count: number
  skill_count: number
  artifact_counts: ArtifactCounts
  local_tables: string[]
  default_connection: string
  read_only: boolean
}

// A2UIQueryResult mirrors studio.A2UIQueryResult — the result of one A2UI
// data-bound component's live "sql" binding (RunA2UIQuery), gated through the
// agent sandbox's per-statement SQL classifier, not a bypass.
interface A2UIQueryResult {
  columns: string[]
  types: string[]
  rows: any[][]
  // true when the query had more rows than the 500-row cap (Go caps; never capped again here)
  truncated?: boolean
  receipt_id?: string // the run's receipt; absent when none was kept
  receipt_note?: ReceiptNote // why there is none when one was due
}

// RenderSource mirrors studio.RenderSource: which notebook cell or A2UI component a
// RunA2UIQueryIn run is for. Go checks it against the open notebook or artifact and
// drops one that does not match. The zero value ({kind: ''}) is a render with no source.
interface RenderSource {
  kind: '' | 'cell' | 'a2ui'
  notebook?: string
  artifact?: string
  cell?: string
  component?: string
}

// Why a run that should have a receipt has none: its receipt could not be written
// ("not_kept"), or this workspace keeps none ("unavailable"). A class, never an error.
type ReceiptNote = 'not_kept' | 'unavailable'

// ReceiptRecord mirrors receipts.Receipt: what was asked and what it touched, never
// a result value. ReceiptSummary mirrors receipts.Summary (one line of a listing).
interface ReceiptSnapshot { fact: string; value: string; label: 'replayable' | 'live_only' | 'not_recorded' }
interface ReceiptConnection {
  name: string
  type: string
  read_only: boolean
  status: 'connected' | 'not_connected' | 'error'
  snapshot?: ReceiptSnapshot
}
interface ReceiptParam { value?: string; withheld?: boolean; truncated?: boolean }
interface ReceiptRecord {
  id: string
  at: string
  duration_ms: number
  surface: string
  actor: 'person' | 'agent'
  agent_session?: string
  source: { kind?: string; ref?: string }
  sql?: string
  sql_withheld?: boolean
  run_sql?: string
  run_sql_withheld?: boolean
  sql_hash?: string
  run_sql_hash?: string
  params?: Record<string, ReceiptParam>
  params_hash?: string
  params_withheld?: boolean
  connections?: ReceiptConnection[]
  verdict: 'read' | 'unchecked'
  rows: { returned: number; total: number; truncated: boolean }
  columns?: { name: string; type: string; withheld?: boolean }[]
  result_hmac: string
}
interface ReceiptSummary {
  id: string
  at: string
  duration_ms: number
  surface: string
  actor: 'person' | 'agent'
  source: { kind?: string; ref?: string }
  verdict: 'read' | 'unchecked'
  rows: { returned: number; total: number; truncated: boolean }
  connections?: string[]
  sql?: string
  sql_withheld?: boolean
}
interface ReceiptFilter { surface: string; limit: number }
// capped: the scan stopped early (the newest 2,000 receipts), so an empty or short list is not "none"
interface ReceiptListing { receipts: ReceiptSummary[]; capped: boolean }

// ArtifactSummary mirrors studio.ArtifactSummary (#162): one data artifact in
// the Library. A folder that could not be read still lists, with `error`.
interface ArtifactSummary {
  name: string
  title: string
  description: string
  view: string
  updated_at: string
  rev: string
  error?: string
}

// ArtifactParamT mirrors artifacts.Param (the manifest's parameter declaration).
interface ArtifactParamT {
  name: string
  type: 'text' | 'number' | 'integer' | 'date' | 'boolean' | 'enum'
  label?: string
  default: string | null
  max_length?: number
  min?: number
  max?: number
  options?: string[]
  options_query?: string
}

// ArtifactDetail mirrors studio.ArtifactDetail: an artifact opened. `view` is the
// page Go validated and re-marshalled; `defaults` the resolved default values.
interface ArtifactDetail {
  name: string
  title: string
  description: string
  params: ArtifactParamT[]
  queries: { name: string; uses: string[] }[]
  /** 'a2ui', 'script' or 'html' A script view or an HTML page has no `view`: it is shown as source until allowed. */
  view_kind: 'a2ui' | 'script' | 'html'
  /** An HTML page's derived host. */
  page_kind?: 'static' | 'scripted'
  view?: { title: string; components: import('./a2ui/types').A2UIComponentT[] }
  defaults: Record<string, string | number | boolean>
  rev: string
  updated_at: string
  error?: string
}

// ArtifactSource mirrors studio.ArtifactSource (#162): an artifact's two files as
// text, as on disk (valid or not), with the one revision over both.
interface ArtifactSource {
  name: string
  manifest: string
  view: string
  /** The view file's name: view.a2ui.json or view.js. */
  view_file: string
  manifest_missing: boolean
  view_missing: boolean
  rev: string
  updated_at: string
}

// ArtifactSaveResult mirrors studio.ArtifactSaveResult: a save that landed.
// `warnings` are queries the agent's read policy refuses (the policy's reason).
interface ArtifactSaveResult {
  rev: string
  updated_at: string
  warnings: string[]
}

// Mirrors internal/importer's Type/ColType/Source/Inspection/Column/Options/
// Summary/BadValue (#82). Keep in sync with internal/importer/*.go and
// lib/importTypes.ts.
type ImportFriendlyType = 'text' | 'integer' | 'decimal' | 'number' | 'date' | 'timestamp' | 'boolean'

interface ImportColType {
  type: ImportFriendlyType
  scale?: number
}

interface ImportSource {
  path: string
  sheet?: string
  has_header: boolean
}

interface ImportColumn {
  name: string
  source_name: string
  inferred: ImportColType
  candidate: boolean
}

interface ImportInspection {
  sheets?: string[]
  sheet?: string
  columns: ImportColumn[]
  rows: (string | null)[][]
  row_count: number
  suggested_key: string
}

type ImportMode = 'create' | 'replace' | 'append' | 'upsert'

interface ImportOptions extends ImportSource {
  table: string
  mode?: ImportMode | ''
  key?: string
  types?: Record<string, ImportColType>
  dry_run?: boolean
}

interface ImportBadValue {
  row: number
  column: string
  value: string
  want: string
  reason: 'type' | 'missing_parent' | 'duplicate_key' | 'null_key'
}

interface ImportSummary {
  ok: boolean
  mode: ImportMode | ''
  table: string
  exists: boolean
  existing_rows: number
  table_key: string
  modes: ImportMode[]
  rows: number
  inserted: number
  updated: number
  unchanged: number
  replaced: number
  exact_dupes: number
  missing: string[] | null
  extra: string[] | null
  bad_values: ImportBadValue[] | null
  bad_count: number
  blocked: string
}


// --- .slaw bundles (#118): mirrors studio/bundle.go and internal/bundle ---

interface BundleContentItem { id: string; title: string }
interface BundleContentConnection {
  name: string; type: string; summary: string
  scope: 'team' | 'this computer'
  portable: boolean; reason?: string
}
interface BundleContent {
  workspaceName: string
  notebooks: BundleContentItem[]
  queries: BundleContentItem[]
  skills: BundleContentItem[]
  artifacts: BundleContentItem[]
  semantics: BundleContentItem[]
  watches: BundleContentItem[]
  connections: BundleContentConnection[]
}
interface BundleSelection {
  all: boolean
  notebooks: string[]; queries: string[]; skills: string[]; artifacts: string[]; semantics: string[]; watches: string[]
  sharedConnections: boolean
  teamConnections: string[]
  localConnections: string[]
}
interface BundleExportRequest { path: string; selection: BundleSelection; passphrase: string }
interface BundleExportResult {
  path: string; cancelled: boolean; encrypted: boolean
  notebooks: number; queries: number; skills: number; artifacts: number; semantics: number; watches: number; connections: number
  bytes: number; warnings: string[]
}
type BundleAction = 'rename' | 'overwrite' | 'skip'
interface BundlePreviewItem { key: string; kind: 'notebook' | 'query' | 'skill' | 'artifact' | 'semantics' | 'watch'; id: string; title: string; size: number; conflict: boolean; noRename?: boolean }
interface BundlePreviewConnection {
  key: string; name: string; type: string; summary: string
  details: string[]; risks: string[]; askedWritable: boolean
  conflict: boolean; conflictScope?: string; renamedTo?: string
}
interface BundlePreview {
  handle: string
  needsPassphrase: boolean
  wrongPassphrase: boolean
  path: string
  workspaceName: string; createdAt: string; createdBy: string; encrypted: boolean
  items: BundlePreviewItem[]
  connections: BundlePreviewConnection[]
  warnings: string[]
  makerNotes: string[]
}
interface BundleDecisions { onConflict: BundleAction; items: Record<string, BundleAction>; connections: string[] }
interface BundleOutcome { key: string; kind: string; id: string; finalId?: string; result: string; error?: string }
interface BundleResult { outcomes: BundleOutcome[]; dryRun: boolean; warnings: string[] }

// Updates: the saved choice and install kind (no network), one check's result, and a finished upgrade.
interface UpdateState {
  check: '' | 'off' | 'weekly'; lastChecked: string; skippedVersion: string
  version: string; isDev: boolean; needsConsent: boolean; managed: string; updatable: boolean; managedNote: string
  appliedVersion: string; canRestart: boolean
}
interface UpdateResult {
  current: string; latest: string; updateAvailable: boolean; skipped: boolean; releaseUrl: string
  notes: string; managed: string; updatable: boolean; managedNote: string; checkedAt: string
}
interface UpdateApplyResult { from: string; to: string; output: string; studio: boolean; restart: string }

// Settings > About (#40): what GetBuildInfo returns.
interface BuildInfo { version: string; commit: string; date: string; goVersion: string; duckdbVersion: string; platform: string }

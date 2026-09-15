/** Mirrors the JSON the Go API returns. Kept by hand so a field that changes
 *  shape on the server fails the frontend build rather than at runtime. */

export type Algorithm =
  | 'round_robin'
  | 'weighted_round_robin'
  | 'least_connections'
  | 'ip_hash'

export type CertSource = 'acme' | 'manual' | 'self_signed'
export type ChallengeType = 'http-01' | 'tls-alpn-01' | 'dns-01'
export type Role = 'admin' | 'viewer'

export interface Upstream {
  id: number
  hostId: number
  scheme: 'http' | 'https'
  address: string
  weight: number
  maxConns: number
  enabled: boolean
  skipTlsVerify: boolean
}

export interface HealthCheck {
  enabled: boolean
  path: string
  /** Nanoseconds: Go encodes time.Duration as an integer count of them. */
  interval: number
  timeout: number
  healthyThreshold: number
  unhealthyThreshold: number
  expectStatus: number
}

/** PassiveHealth ejects a backend after repeated connection failures on real
 *  traffic, independently of the active probe. */
export interface PassiveHealth {
  enabled: boolean
  maxFails: number
  /** Nanoseconds, as Go encodes time.Duration. */
  ejectFor: number
}

export interface Host {
  id: number
  name: string
  enabled: boolean
  domains: string[]
  algorithm: Algorithm
  upstreams: Upstream[]
  certificateId: number | null
  accessListId: number | null
  forceHttps: boolean
  hstsMaxAge: number
  websocketSupport: boolean
  preserveHost: boolean
  healthCheck: HealthCheck
  passiveHealth: PassiveHealth
  accessLog: AccessLogSettings
  createdAt: string
  updatedAt: string
}

export interface Certificate {
  id: number
  name: string
  domains: string[]
  source: CertSource
  issuer: string
  notBefore: string
  notAfter: string
  challenge?: ChallengeType
  dnsProvider?: string
  lastError?: string
  lastIssued?: string
  expiresInDays: number
  installed: boolean
  inUseByHosts: number
}

export interface DnsProviderField {
  key: string
  label: string
  required: boolean
  secret: boolean
  help?: string
}

export interface DnsProvider {
  name: string
  label: string
  description: string
  fields: DnsProviderField[]
}

export interface AlgorithmOption {
  value: Algorithm
  label: string
  description: string
}

export interface User {
  id: number
  username: string
  role: Role
  createdAt: string
  lastLoginAt?: string
}

/* ------------------------------------------------------------ live feed -- */

export interface UpstreamSnapshot {
  upstreamId: number
  address: string
  healthy: boolean
  /** Taken out of rotation by passive health after repeated connection
   *  failures. Distinct from `healthy`, which is the active probe's verdict. */
  ejected: boolean
  ejectedForSeconds?: number
  enabled: boolean
  weight: number
  activeConns: number
  totalRequests: number
  meanLatencyMs: number
  lastError?: string
}

export interface TrafficSnapshot {
  requestsPerSec: number
  bytesInPerSec: number
  bytesOutPerSec: number
  meanLatencyMs: number
  errorRate: number
  activeConns: number
}

export interface HostSnapshot {
  hostId: number
  name: string
  traffic: TrafficSnapshot
  upstreams: UpstreamSnapshot[] | null
}

export interface SystemSnapshot {
  uptimeSeconds: number
  goroutines: number
  heapBytes: number
  cpuPercent: number
  hostsEnabled: number
  upstreamsUp: number
  upstreamsTotal: number
}

export interface Snapshot {
  timestamp: string
  hosts: HostSnapshot[] | null
  totals: TrafficSnapshot
  system: SystemSnapshot
}

/* -------------------------------------------------------------- history -- */

export type Resolution = 'minute' | 'hour' | 'day'

export interface SeriesPoint {
  timestamp: string
  requests: number
  requestsPerSec: number
  status2xx: number
  status3xx: number
  status4xx: number
  status5xx: number
  statusError: number
  bytesIn: number
  bytesOut: number
  meanLatencyMs: number
  maxLatencyMs: number
  errorRate: number
}

export interface Series {
  hostId: number
  from: string
  to: string
  resolution: Resolution
  points: SeriesPoint[] | null
}

/* --------------------------------------------------------------- writes -- */

export interface UpstreamInput {
  scheme: 'http' | 'https'
  address: string
  weight: number
  maxConns: number
  enabled: boolean
  skipTlsVerify: boolean
}

export interface HostInput {
  name: string
  enabled: boolean
  domains: string[]
  algorithm: Algorithm
  upstreams: UpstreamInput[]
  certificateId: number | null
  accessListId: number | null
  forceHttps: boolean
  hstsMaxAge: number
  websocketSupport: boolean
  preserveHost: boolean
  healthCheck: {
    enabled: boolean
    path: string
    intervalSeconds: number
    timeoutSeconds: number
    healthyThreshold: number
    unhealthyThreshold: number
    expectStatus: number
  }
  passiveHealth: {
    enabled: boolean
    maxFails: number
    ejectForSeconds: number
  }
  accessLog: AccessLogSettings
}

export interface CertificateInput {
  name: string
  domains: string[]
  source: CertSource
  certificatePem?: string
  privateKeyPem?: string
  challenge?: ChallengeType
  dnsProvider?: string
  dnsCredentials?: Record<string, string>
}

export interface FieldError {
  field: string
  message: string
}

/* --------------------------------------------------------- access lists -- */

export type AccessAction = 'allow' | 'deny'

export interface AccessRule {
  id: number
  accessListId: number
  action: AccessAction
  cidr: string
}

/** The password hash is never sent to the console, only compared against by
 *  the proxy, so this carries the username alone. */
export interface BasicAuthUser {
  id: number
  accessListId: number
  username: string
}

export interface AccessList {
  id: number
  name: string
  satisfyAny: boolean
  rules: AccessRule[] | null
  basicAuth: BasicAuthUser[] | null
  inUseByHosts: number
  createdAt: string
  updatedAt: string
}

export interface AccessRuleInput {
  action: AccessAction
  cidr: string
}

/** An empty password on an update keeps the stored one, which is the only way
 *  to edit a list without retyping every credential in it. */
export interface BasicAuthInput {
  username: string
  password?: string
}

export interface AccessListInput {
  name: string
  satisfyAny: boolean
  rules: AccessRuleInput[]
  basicAuth: BasicAuthInput[]
}

/* ----------------------------------------------------------- access log -- */

/** Logging is off per host by default: a busy proxy would otherwise fill a
 *  disk with rows nobody asked for. */
export interface AccessLogSettings {
  enabled: boolean
  /** Adds the query string to the recorded path. Off by default, because
   *  query strings routinely carry tokens and this log is searchable from
   *  the console. */
  includeQuery: boolean
}

export interface AccessLogEntry {
  id: number
  timestamp: string
  hostId: number
  method: string
  path: string
  status: number
  durationMs: number
  bytesOut: number
  clientIp: string
  upstream: string
  userAgent: string
  error?: string
}

/** What the background writer has managed to store. A quiet host and a writer
 *  that is dropping entries look identical without this. */
export interface AccessLogStats {
  written: number
  dropped: number
  pending: number
}

export interface AccessLogPage {
  entries: AccessLogEntry[] | null
  total: number
  limit: number
  offset: number
  stats: AccessLogStats
}

export interface AccessLogFilters {
  hostId?: number
  statusClass?: number
  search?: string
  failedOnly?: boolean
  from?: Date
  to?: Date
  limit?: number
  offset?: number
}

/* ---------------------------------------------------------------- users -- */

export interface UserInput {
  username: string
  password: string
  role: Role
}

/* --------------------------------------------------------------- alerts -- */

export type AlertEvent =
  | 'upstream_down'
  | 'upstream_recovered'
  | 'upstream_ejected'
  | 'host_unavailable'
  | 'certificate_expiring'
  | 'certificate_failed'

export interface AlertEventOption {
  value: AlertEvent
  label: string
  severity: string
  description: string
}

export interface AlertChannel {
  id: number
  name: string
  type: 'webhook'
  enabled: boolean
  /** Masked. The real URL is never sent to the console: for a chat webhook
   *  the URL is itself the credential. */
  url: string
  events: AlertEvent[] | null
  /** Nanoseconds, as Go encodes time.Duration. */
  minInterval: number
  minIntervalSeconds: number
  lastAttempt?: string
  lastError?: string
  createdAt: string
  updatedAt: string
}

export interface AlertChannelInput {
  name: string
  type: 'webhook'
  enabled: boolean
  /** Empty on an update keeps the stored URL, which is the only way to edit
   *  a channel without retyping a secret the console never showed you. */
  url: string
  events: AlertEvent[]
  minIntervalSeconds: number
}

export interface AlertStats {
  sent: number
  failed: number
  dropped: number
}

export interface AlertTestResult {
  delivered: boolean
  error?: string
}

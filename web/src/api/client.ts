import type {
  AccessList,
  AccessListInput,
  AccessLogFilters,
  AccessLogPage,
  AlertChannel,
  AlertChannelInput,
  AlertEventOption,
  AlertStats,
  AlertTestResult,
  AlgorithmOption,
  Certificate,
  CertificateInput,
  DnsProvider,
  GuardianRuleOption,
  FieldError,
  Host,
  HostInput,
  Resolution,
  Role,
  Series,
  Snapshot,
  User,
  UserInput,
} from './types'

/** ApiError carries the server's own explanation, including per-field
 *  validation messages, so a form can mark the inputs that failed instead of
 *  showing one opaque sentence. */
export class ApiError extends Error {
  readonly status: number
  readonly fields: FieldError[]

  constructor(status: number, message: string, fields: FieldError[] = []) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.fields = fields
  }

  /** True when the session has expired or the token was rejected. */
  get isUnauthorized(): boolean {
    return this.status === 401
  }

  fieldMessage(name: string): string | undefined {
    return this.fields.find((f) => f.field === name)?.message
  }
}

const TOKEN_KEY = 'ponzproxy.token'

/** The token lives in localStorage so a reload does not sign the operator out.
 *  That is a deliberate trade: the console is an internal tool, and the
 *  alternative — re-entering a password on every refresh — pushes people
 *  towards weaker passwords. */
export const session = {
  get(): string | null {
    try {
      return localStorage.getItem(TOKEN_KEY)
    } catch {
      return null
    }
  },
  set(token: string): void {
    try {
      localStorage.setItem(TOKEN_KEY, token)
    } catch {
      /* Private browsing: the session simply lasts until the tab closes. */
    }
  },
  clear(): void {
    try {
      localStorage.removeItem(TOKEN_KEY)
    } catch {
      /* ignore */
    }
  },
}

/** onUnauthorized is invoked whenever the server rejects the session, so the
 *  app can return to the sign-in screen from anywhere without every caller
 *  handling it. */
let onUnauthorized: (() => void) | null = null

export function setUnauthorizedHandler(fn: (() => void) | null): void {
  onUnauthorized = fn
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  const token = session.get()
  if (token) headers.set('Authorization', `Bearer ${token}`)
  if (init.body !== undefined) headers.set('Content-Type', 'application/json')

  let response: Response
  try {
    response = await fetch(path, { ...init, headers })
  } catch {
    // A network failure here means the console cannot reach ponzproxy at
    // all, which is a different problem from a rejected request.
    throw new ApiError(0, 'Cannot reach ponzproxy. Check that the service is running.')
  }

  if (response.status === 204) return undefined as T

  const text = await response.text()
  const body: unknown = text ? safeParse(text) : null

  if (!response.ok) {
    if (response.status === 401) {
      session.clear()
      onUnauthorized?.()
    }
    const shaped = body as { error?: string; fields?: FieldError[] } | null
    throw new ApiError(
      response.status,
      shaped?.error ?? `Request failed with status ${response.status}.`,
      shaped?.fields ?? [],
    )
  }

  return body as T
}

function safeParse(text: string): unknown {
  try {
    return JSON.parse(text)
  } catch {
    return null
  }
}

export interface LoginResult {
  token: string
  expiresAt: string
  user: User
}

export const api = {
  login: (username: string, password: string) =>
    request<LoginResult>('/api/auth/login', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    }),

  me: () => request<User>('/api/auth/me'),

  changePassword: (currentPassword: string, newPassword: string) =>
    request<void>('/api/auth/password', {
      method: 'POST',
      body: JSON.stringify({ currentPassword, newPassword }),
    }),

  listAlertChannels: () => request<AlertChannel[]>('/api/alert-channels'),

  listAlertEvents: () => request<AlertEventOption[]>('/api/alert-events'),

  alertStats: () => request<AlertStats>('/api/alert-stats'),

  createAlertChannel: (input: AlertChannelInput) =>
    request<AlertChannel>('/api/alert-channels', {
      method: 'POST',
      body: JSON.stringify(input),
    }),

  updateAlertChannel: (id: number, input: AlertChannelInput) =>
    request<AlertChannel>(`/api/alert-channels/${id}`, {
      method: 'PUT',
      body: JSON.stringify(input),
    }),

  deleteAlertChannel: (id: number) =>
    request<void>(`/api/alert-channels/${id}`, { method: 'DELETE' }),

  testAlertChannel: (id: number) =>
    request<AlertTestResult>(`/api/alert-channels/${id}/test`, { method: 'POST' }),

  listUsers: () => request<User[]>('/api/users'),

  createUser: (input: UserInput) =>
    request<User>('/api/users', { method: 'POST', body: JSON.stringify(input) }),

  updateUserRole: (id: number, role: Role) =>
    request<void>(`/api/users/${id}/role`, {
      method: 'PUT',
      body: JSON.stringify({ role }),
    }),

  resetUserPassword: (id: number, newPassword: string) =>
    request<void>(`/api/users/${id}/password`, {
      method: 'POST',
      body: JSON.stringify({ newPassword }),
    }),

  deleteUser: (id: number) => request<void>(`/api/users/${id}`, { method: 'DELETE' }),

  listHosts: () => request<Host[]>('/api/hosts'),

  createHost: (input: HostInput) =>
    request<Host>('/api/hosts', { method: 'POST', body: JSON.stringify(input) }),

  updateHost: (id: number, input: HostInput) =>
    request<Host>(`/api/hosts/${id}`, { method: 'PUT', body: JSON.stringify(input) }),

  deleteHost: (id: number) => request<void>(`/api/hosts/${id}`, { method: 'DELETE' }),

  listAccessLists: () => request<AccessList[]>('/api/access-lists'),

  createAccessList: (input: AccessListInput) =>
    request<AccessList>('/api/access-lists', {
      method: 'POST',
      body: JSON.stringify(input),
    }),

  updateAccessList: (id: number, input: AccessListInput) =>
    request<AccessList>(`/api/access-lists/${id}`, {
      method: 'PUT',
      body: JSON.stringify(input),
    }),

  deleteAccessList: (id: number) =>
    request<void>(`/api/access-lists/${id}`, { method: 'DELETE' }),

  listGuardianRules: () => request<GuardianRuleOption[]>('/api/guardian-rules'),

  listAlgorithms: () => request<AlgorithmOption[]>('/api/algorithms'),

  listCertificates: () => request<Certificate[]>('/api/certificates'),

  createCertificate: (input: CertificateInput) =>
    request<Certificate>('/api/certificates', {
      method: 'POST',
      body: JSON.stringify(input),
    }),

  issueCertificate: (id: number) =>
    request<{ status: string; detail: string }>(`/api/certificates/${id}/issue`, {
      method: 'POST',
    }),

  deleteCertificate: (id: number) =>
    request<void>(`/api/certificates/${id}`, { method: 'DELETE' }),

  listDnsProviders: () => request<DnsProvider[]>('/api/dns-providers'),

  accessLog: (filters: AccessLogFilters = {}) => {
    const query = new URLSearchParams()
    if (filters.hostId) query.set('hostId', String(filters.hostId))
    if (filters.statusClass) query.set('statusClass', String(filters.statusClass))
    if (filters.search) query.set('search', filters.search)
    if (filters.failedOnly) query.set('failedOnly', 'true')
    if (filters.from) query.set('from', filters.from.toISOString())
    if (filters.to) query.set('to', filters.to.toISOString())
    if (filters.limit) query.set('limit', String(filters.limit))
    if (filters.offset) query.set('offset', String(filters.offset))
    return request<AccessLogPage>(`/api/access-log?${query.toString()}`)
  },

  liveSnapshot: () => request<Snapshot>('/api/metrics/live'),

  history: (params: { hostId?: number; from: Date; to: Date; resolution: Resolution }) => {
    const query = new URLSearchParams({
      from: params.from.toISOString(),
      to: params.to.toISOString(),
      resolution: params.resolution,
    })
    if (params.hostId) query.set('hostId', String(params.hostId))
    return request<Series>(`/api/metrics/history?${query.toString()}`)
  },
}

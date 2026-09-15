import { useEffect, useState } from 'react'
import { session } from '../api/client'
import type { Snapshot } from '../api/types'

export type LinkState = 'connecting' | 'live' | 'lost'

interface LiveFeed {
  snapshot: Snapshot | null
  link: LinkState
}

/** useLiveFeed subscribes to the server's WebSocket broadcast.
 *
 *  Reconnection backs off: a proxy that is restarting should not be hammered
 *  by every open dashboard, and an operator watching a deploy will leave the
 *  tab open through it. */
export function useLiveFeed(enabled: boolean): LiveFeed {
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null)
  const [link, setLink] = useState<LinkState>('connecting')

  useEffect(() => {
    if (!enabled) return

    let socket: WebSocket | null = null
    let retryTimer: number | undefined
    let attempt = 0
    let closed = false

    const connect = () => {
      if (closed) return

      const token = session.get()
      if (!token) return

      const scheme = location.protocol === 'https:' ? 'wss' : 'ws'
      // The token travels as a query parameter because a browser cannot set
      // headers on a WebSocket handshake.
      socket = new WebSocket(
        `${scheme}://${location.host}/api/ws?token=${encodeURIComponent(token)}`,
      )

      socket.onopen = () => {
        attempt = 0
        setLink('live')
      }

      socket.onmessage = (event) => {
        let parsed: Snapshot
        try {
          parsed = JSON.parse(event.data as string) as Snapshot
        } catch {
          return
        }
        setSnapshot(parsed)
      }

      socket.onclose = () => {
        if (closed) return
        setLink('lost')
        // 1s, 2s, 4s… capped at 15s.
        const delay = Math.min(1000 * 2 ** attempt, 15_000)
        attempt += 1
        retryTimer = window.setTimeout(connect, delay)
      }

      socket.onerror = () => {
        // onclose always follows, and that is where reconnection is handled.
        socket?.close()
      }
    }

    connect()

    return () => {
      closed = true
      window.clearTimeout(retryTimer)
      socket?.close()
    }
  }, [enabled])

  return { snapshot, link }
}

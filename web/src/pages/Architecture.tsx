import { useMemo, useRef, useState } from "react"

import type { AccessList, Certificate, Host, Snapshot } from "@/api/types"
import { Detail } from "@/components/architecture/detail"
import { Legend } from "@/components/architecture/legend"
import { buildTopology } from "@/components/architecture/model"
import { Topology, type Selection } from "@/components/architecture/topology"
import { useElementWidth } from "@/components/architecture/useElementWidth"
import {
  Card,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"

interface Props {
  snapshot: Snapshot | null
  hosts: Host[]
  certificates: Certificate[]
  accessLists: AccessList[]
}

/** Architecture draws the proxy's request path as it is configured right now,
 *  animated by the live feed.
 *
 *  It exists because the same facts spread across four screens — the host
 *  list, the certificate list, the access lists and the upstream table — do
 *  not answer "what happens to a request for this domain, and where is it
 *  going". One picture per request path does. Everything on it is already in
 *  the console; nothing here is a new API call. */
export function Architecture({
  snapshot,
  hosts,
  certificates,
  accessLists,
}: Props) {
  const model = useMemo(
    () => buildTopology(snapshot, hosts, certificates, accessLists),
    [snapshot, hosts, certificates, accessLists],
  )

  const frame = useRef<HTMLDivElement>(null)
  const width = useElementWidth(frame)
  const [selection, setSelection] = useState<Selection | null>(null)

  const measured = model.hosts.some((h) => h.basis === "requests")

  return (
    <div className="px-4 lg:px-6">
      <Card className="gap-0 pb-0">
        <CardHeader>
          <CardTitle>Request path</CardTitle>
          <CardDescription>
            Every host the proxy answers for, what a request passes through on
            the way in, and how the load is actually split across its backends.
          </CardDescription>
        </CardHeader>

        {model.hosts.length === 0 ? (
          <div className="text-muted-foreground px-6 py-12 text-center text-sm">
            <p className="text-foreground font-medium">
              There is nothing to route yet
            </p>
            <p>Add a host and the path from the internet to it appears here.</p>
          </div>
        ) : (
          <>
            <Problems model={model} onSelect={setSelection} />

            {!measured && (
              <p className="text-muted-foreground px-4 pt-4 text-xs sm:px-6">
                No requests have been served yet. Line weight shows each
                backend's configured weight until traffic arrives.
              </p>
            )}

            <div ref={frame} className="overflow-hidden px-4 py-4 sm:px-6">
              <Topology
                model={model}
                width={width}
                selection={selection}
                onSelect={setSelection}
              />
            </div>

            <Legend />
            <Detail
              model={model}
              selection={selection}
              onClear={() => setSelection(null)}
            />
          </>
        )}
      </Card>
    </div>
  )
}

/** Problems restates what the diagram already draws in red.
 *
 *  An operator arriving mid-incident should not have to find the red link
 *  before they can read the reason, and on a proxy with twenty hosts the red
 *  link may be well below the fold. Each row selects the backend it names. */
function Problems({
  model,
  onSelect,
}: {
  model: ReturnType<typeof buildTopology>
  onSelect: (selection: Selection) => void
}) {
  if (model.problems.length === 0) return null

  const shown = model.problems.slice(0, 4)
  const rest = model.problems.length - shown.length

  return (
    <div
      role="alert"
      className="border-destructive/50 bg-destructive/10 mx-4 flex flex-col gap-1 rounded-md border px-3 py-2 text-sm sm:mx-6"
    >
      {shown.map((problem) => (
        <button
          key={`${problem.hostId}:${problem.key}`}
          type="button"
          className="text-destructive text-left hover:underline"
          onClick={() =>
            onSelect({
              kind: "upstream",
              hostId: problem.hostId,
              key: problem.key,
            })
          }
        >
          <span className="font-mono text-xs">{problem.upstream}</span>{" "}
          <span className="text-muted-foreground text-xs">
            on {problem.host}
          </span>{" "}
          — {problem.reason}
        </button>
      ))}
      {rest > 0 && (
        <span className="text-muted-foreground text-xs">
          and {rest} more below.
        </span>
      )}
    </div>
  )
}

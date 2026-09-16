import { cn } from "@/lib/utils"

/** The five links of the chain, named once here so the diagram can get away
 *  with single letters on a 15px square. */
const TRAIL: { letter: string; label: string }[] = [
  { letter: "T", label: "TLS" },
  { letter: "R", label: "HTTPS redirect" },
  { letter: "A", label: "Access list" },
  { letter: "I", label: "Request inspection" },
  { letter: "L", label: "Access log" },
]

/** Legend spells out the four things the diagram encodes without words:
 *  line weight, colour, movement, and the letters in front of each host.
 *
 *  It is deliberately not collapsible. An operator reads this screen once a
 *  month, under pressure, and a legend hidden behind a disclosure is a legend
 *  nobody finds. */
export function Legend() {
  return (
    <div className="flex flex-col gap-3 border-t px-4 py-4 text-xs sm:px-6">
      <div className="flex flex-wrap items-center gap-x-6 gap-y-3">
        <Item
          swatch={
            <svg width="44" height="16" aria-hidden="true" className="shrink-0">
              <line
                x1="2" y1="4" x2="42" y2="4"
                strokeWidth="6" strokeLinecap="round"
                className="stroke-muted-foreground" opacity="0.32"
              />
              <line
                x1="2" y1="10" x2="42" y2="10"
                strokeWidth="2.8" strokeLinecap="round"
                className="stroke-muted-foreground" opacity="0.32"
              />
              <line
                x1="2" y1="14" x2="42" y2="14"
                strokeWidth="1.25" strokeLinecap="round"
                className="stroke-muted-foreground" opacity="0.32"
              />
            </svg>
          }
        >
          Line weight is share of traffic. An even split and a 5:2:1 weighting
          are meant to look different without reading a number.
        </Item>

        <Item
          swatch={
            <svg width="44" height="16" aria-hidden="true" className="shrink-0">
              <line
                x1="2" y1="5" x2="42" y2="5"
                strokeWidth="5" strokeLinecap="round"
                className="stroke-emerald-500" opacity="0.4"
              />
              <line
                x1="2" y1="5" x2="42" y2="5"
                strokeWidth="2.5" strokeLinecap="round" strokeDasharray="4 10"
                className="stroke-emerald-500" opacity="0.9"
              />
              <line
                x1="2" y1="13" x2="42" y2="13"
                strokeWidth="5" strokeLinecap="round"
                className="stroke-muted-foreground" opacity="0.16"
              />
            </svg>
          }
        >
          Green means the link is carrying traffic, and its dashes travel at the
          current request rate in six steps. Grey and still is up but idle.
        </Item>

        <Item
          swatch={
            <svg width="44" height="16" aria-hidden="true" className="shrink-0">
              <line
                x1="2" y1="8" x2="42" y2="8"
                strokeWidth="1.5" strokeDasharray="5 5"
                className="stroke-destructive" opacity="0.65"
              />
            </svg>
          }
        >
          A red dashed link is a backend out of rotation. Hover or click it for
          the reason.
        </Item>
      </div>

      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <span className="text-muted-foreground">
          In front of each host, in the order the proxy applies them:
        </span>
        {TRAIL.map((step) => (
          <span key={step.letter} className="flex items-center gap-1.5">
            <Square className="bg-foreground text-background">{step.letter}</Square>
            <span className="text-muted-foreground">{step.label}</span>
          </span>
        ))}
      </div>

      <div className="text-muted-foreground flex flex-wrap items-center gap-x-4 gap-y-2">
        <span className="flex items-center gap-1.5">
          <Square className="bg-foreground text-background">T</Square> on
        </span>
        <span className="flex items-center gap-1.5">
          <Square className="border-border text-muted-foreground border bg-transparent">
            T
          </Square>{" "}
          off
        </span>
        <span className="flex items-center gap-1.5">
          <Square className="border-amber-500 bg-amber-500/20 text-amber-600 dark:text-amber-400 border">
            T
          </Square>{" "}
          needs attention
        </span>
        <span className="flex items-center gap-1.5">
          <Square className="border-destructive bg-destructive/15 text-destructive border">
            T
          </Square>{" "}
          broken
        </span>
        <span>Hover any square for what it is set to.</span>
      </div>
    </div>
  )
}

function Item({
  swatch,
  children,
}: {
  swatch: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <span className="flex max-w-xs items-start gap-2">
      {swatch}
      <span className="text-muted-foreground">{children}</span>
    </span>
  )
}

function Square({
  className,
  children,
}: {
  className?: string
  children: React.ReactNode
}) {
  return (
    <span
      className={cn(
        "inline-flex size-[15px] shrink-0 items-center justify-center rounded-[3.5px] text-[9px] font-semibold",
        className,
      )}
    >
      {children}
    </span>
  )
}

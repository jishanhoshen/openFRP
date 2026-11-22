import { useId } from "react";

export interface AreaPoint {
  label: string;
  a: number;
  b: number;
}

interface Props {
  data: AreaPoint[];
  aName: string;
  bName: string;
  format: (n: number) => string;
  height?: number;
}

const W = 640;
const PAD = { top: 12, right: 12, bottom: 26, left: 12 };

/** Two-series area chart drawn as SVG. No charting library, no inline styles. */
export function AreaChart({ data, aName, bName, format, height = 240 }: Props) {
  const uid = useId().replace(/:/g, "");
  if (data.length === 0) return null;

  const H = height;
  const innerW = W - PAD.left - PAD.right;
  const innerH = H - PAD.top - PAD.bottom;
  const max = Math.max(1, ...data.flatMap((d) => [d.a, d.b]));
  const x = (i: number) => PAD.left + (data.length === 1 ? innerW / 2 : (i * innerW) / (data.length - 1));
  const y = (v: number) => PAD.top + innerH - (v / max) * innerH;
  const base = PAD.top + innerH;

  const line = (key: "a" | "b") =>
    data.map((d, i) => `${i === 0 ? "M" : "L"}${x(i).toFixed(1)},${y(d[key]).toFixed(1)}`).join(" ");
  const area = (key: "a" | "b") =>
    `${line(key)} L${x(data.length - 1).toFixed(1)},${base} L${x(0).toFixed(1)},${base} Z`;

  const ticks = [0, 0.5, 1].map((t) => ({ v: max * t, y: y(max * t) }));

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-4 text-xs text-muted-foreground">
        <span className="flex items-center gap-2"><span className="size-2.5 rounded-sm bg-primary" />{aName}</span>
        <span className="flex items-center gap-2"><span className="size-2.5 rounded-sm bg-success" />{bName}</span>
      </div>
      <svg viewBox={`0 0 ${W} ${H}`} className="w-full" role="img" aria-label={`${aName} and ${bName} over time`}>
        <defs>
          <linearGradient id={`${uid}-a`} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor="hsl(var(--primary))" stopOpacity="0.35" />
            <stop offset="100%" stopColor="hsl(var(--primary))" stopOpacity="0" />
          </linearGradient>
          <linearGradient id={`${uid}-b`} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor="hsl(var(--success))" stopOpacity="0.3" />
            <stop offset="100%" stopColor="hsl(var(--success))" stopOpacity="0" />
          </linearGradient>
        </defs>
        {ticks.map((t) => (
          <g key={t.v}>
            <line x1={PAD.left} x2={W - PAD.right} y1={t.y} y2={t.y} stroke="hsl(var(--border))" strokeDasharray="4 4" />
            <text x={W - PAD.right} y={t.y - 4} textAnchor="end" fontSize="10" fill="hsl(var(--muted-foreground))">
              {format(t.v)}
            </text>
          </g>
        ))}
        <path d={area("b")} fill={`url(#${uid}-b)`} />
        <path d={line("b")} fill="none" stroke="hsl(var(--success))" strokeWidth="2" />
        <path d={area("a")} fill={`url(#${uid}-a)`} />
        <path d={line("a")} fill="none" stroke="hsl(var(--primary))" strokeWidth="2" />
        <text x={PAD.left} y={H - 6} fontSize="10" fill="hsl(var(--muted-foreground))">{data[0].label}</text>
        <text x={W - PAD.right} y={H - 6} textAnchor="end" fontSize="10" fill="hsl(var(--muted-foreground))">
          {data[data.length - 1].label}
        </text>
      </svg>
    </div>
  );
}

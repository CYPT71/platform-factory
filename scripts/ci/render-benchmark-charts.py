#!/usr/bin/env python3
"""Render payload-size -> throughput/memory/time charts from benchmark.json
as self-contained SVG - no matplotlib/numpy dependency, consistent with
this project's stdlib-only-tooling convention. Follows the project's
dataviz mark specs: 2px lines, >=8px ring-backed markers, direct end
labels (text never in the series color), a legend for 3 series, and the
validated three-slot categorical palette (blue/orange/aqua) that passes
all-pairs CVD and normal-vision separation in both light and dark mode.
"""
import json
import math
import pathlib
import sys

SERIES_COLORS = {
    "BenchmarkBuild": "#2a78d6",
    "BenchmarkMakeLayer": "#eb6834",
    "BenchmarkNaiveTarGzip": "#1baf7a",
}
SERIES_LABELS = {
    "BenchmarkBuild": "Build (end-to-end)",
    "BenchmarkMakeLayer": "MakeLayer (in-memory)",
    "BenchmarkNaiveTarGzip": "NaiveTarGzip (reference)",
}
SURFACE = "#fcfcfb"
GRID = "#e3e2dc"
TEXT_PRIMARY = "#0b0b0b"
TEXT_SECONDARY = "#52514e"

WIDTH, HEIGHT = 640, 400
MARGIN_LEFT, MARGIN_RIGHT, MARGIN_TOP, MARGIN_BOTTOM = 64, 128, 40, 48
PLOT_W = WIDTH - MARGIN_LEFT - MARGIN_RIGHT
PLOT_H = HEIGHT - MARGIN_TOP - MARGIN_BOTTOM

SIZE_LABELS = {1: "1K", 4: "4K", 64: "64K", 256: "256K", 1024: "1M", 4096: "4M",
               16384: "16M", 32768: "32M", 65536: "64M", 131072: "128M"}


def nice_log_ticks(min_v, max_v):
    """Powers-of-ten-ish ticks (1/2/5 * 10^n) spanning [min_v, max_v]."""
    if min_v <= 0:
        min_v = 1e-9
    ticks = []
    exponent = math.floor(math.log10(min_v))
    while 10 ** exponent <= max_v * 1.01:
        for mult in (1, 2, 5):
            value = mult * (10 ** exponent)
            if min_v * 0.99 <= value <= max_v * 1.01:
                ticks.append(value)
        exponent += 1
    return ticks or [min_v, max_v]


def render_chart(title, y_label, series, y_log, output_path):
    all_x = sorted({kib for points in series.values() for kib, _ in points})
    x_log = {kib: math.log2(kib) for kib in all_x}
    x_min, x_max = x_log[all_x[0]], x_log[all_x[-1]]

    all_y = [v for points in series.values() for _, v in points]
    if y_log:
        y_ticks = nice_log_ticks(min(all_y), max(all_y))
        y_min, y_max = math.log10(y_ticks[0]), math.log10(y_ticks[-1])
        def y_pos(v):
            return MARGIN_TOP + PLOT_H * (1 - (math.log10(v) - y_min) / (y_max - y_min))
        tick_items = [(t, y_pos(t)) for t in y_ticks]
    else:
        y_max = max(all_y) * 1.1
        y_min = 0
        def y_pos(v):
            return MARGIN_TOP + PLOT_H * (1 - (v - y_min) / (y_max - y_min))
        step = y_max / 5
        tick_items = [(step * i, y_pos(step * i)) for i in range(6)]

    def x_pos(kib):
        return MARGIN_LEFT + PLOT_W * (x_log[kib] - x_min) / (x_max - x_min)

    parts = [
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{WIDTH}" height="{HEIGHT}" '
        f'viewBox="0 0 {WIDTH} {HEIGHT}" font-family="-apple-system,Helvetica,Arial,sans-serif">',
        f'<rect x="0" y="0" width="{WIDTH}" height="{HEIGHT}" fill="{SURFACE}"/>',
        f'<text x="{MARGIN_LEFT}" y="20" font-size="15" font-weight="600" fill="{TEXT_PRIMARY}">{title}</text>',
    ]

    # Gridlines + y-axis labels (recessive, hairline, drawn before data).
    for value, y in tick_items:
        label = f"{value:,.0f}" if value >= 1 else f"{value:.2f}"
        parts.append(f'<line x1="{MARGIN_LEFT}" y1="{y:.1f}" x2="{WIDTH - MARGIN_RIGHT}" y2="{y:.1f}" '
                      f'stroke="{GRID}" stroke-width="1"/>')
        parts.append(f'<text x="{MARGIN_LEFT - 8}" y="{y + 4:.1f}" font-size="11" text-anchor="end" '
                      f'fill="{TEXT_SECONDARY}">{label}</text>')
    parts.append(f'<text x="{MARGIN_LEFT}" y="{MARGIN_TOP - 12}" font-size="11" fill="{TEXT_SECONDARY}">{y_label}</text>')

    # X-axis ticks (payload size, log2-spaced - the real spacing, not categorical).
    for kib in all_x:
        x = x_pos(kib)
        parts.append(f'<line x1="{x:.1f}" y1="{MARGIN_TOP + PLOT_H}" x2="{x:.1f}" y2="{MARGIN_TOP + PLOT_H + 4}" '
                      f'stroke="{TEXT_SECONDARY}" stroke-width="1"/>')
        parts.append(f'<text x="{x:.1f}" y="{MARGIN_TOP + PLOT_H + 18}" font-size="10" text-anchor="middle" '
                      f'fill="{TEXT_SECONDARY}">{SIZE_LABELS.get(kib, str(kib))}</text>')
    parts.append(f'<text x="{MARGIN_LEFT + PLOT_W/2:.1f}" y="{HEIGHT - 6}" font-size="11" text-anchor="middle" '
                  f'fill="{TEXT_SECONDARY}">Payload size</text>')

    # One line + ringed markers per series, then a direct end label (never
    # colored text - a small colored dot carries identity beside it).
    for family, points in series.items():
        color = SERIES_COLORS[family]
        points = sorted(points)
        coords = [(x_pos(kib), y_pos(v)) for kib, v in points]
        path = " ".join(f"{'M' if i == 0 else 'L'}{x:.1f},{y:.1f}" for i, (x, y) in enumerate(coords))
        parts.append(f'<path d="{path}" fill="none" stroke="{color}" stroke-width="2" '
                      f'stroke-linejoin="round" stroke-linecap="round"/>')
        for x, y in coords:
            parts.append(f'<circle cx="{x:.1f}" cy="{y:.1f}" r="5" fill="{color}" stroke="{SURFACE}" stroke-width="2"/>')
        last_x, last_y = coords[-1]
        parts.append(f'<circle cx="{last_x + 12:.1f}" cy="{last_y:.1f}" r="4" fill="{color}"/>')
        parts.append(f'<text x="{last_x + 20:.1f}" y="{last_y + 4:.1f}" font-size="11" '
                      f'fill="{TEXT_PRIMARY}">{SERIES_LABELS.get(family, family)}</text>')

    parts.append("</svg>")
    pathlib.Path(output_path).write_text("\n".join(parts) + "\n")


def main():
    if len(sys.argv) != 3:
        raise SystemExit("usage: render-benchmark-charts.py benchmark.json OUTPUT_DIR")
    report = json.loads(pathlib.Path(sys.argv[1]).read_text())
    out_dir = pathlib.Path(sys.argv[2])
    out_dir.mkdir(parents=True, exist_ok=True)

    throughput, memory, time_ = {}, {}, {}
    for family in ("BenchmarkBuild", "BenchmarkMakeLayer", "BenchmarkNaiveTarGzip"):
        entries = report["families"].get(family, [])
        payload_entries = [e for e in entries if "payload_kib" in e and e["payload_kib"] is not None]
        if not payload_entries:
            continue
        throughput[family] = [(e["payload_kib"], e["mb_per_second"]["median"]) for e in payload_entries if "mb_per_second" in e]
        memory[family] = [(e["payload_kib"], e["bytes_per_op"]["median"] / 1024) for e in payload_entries]
        time_[family] = [(e["payload_kib"], e["ns_per_op"]["median"] / 1_000_000) for e in payload_entries]

    if not any(throughput.values()):
        raise SystemExit("no payload-based BenchmarkBuild/MakeLayer/NaiveTarGzip samples found")

    render_chart("Payload size vs. throughput", "MB/s", throughput, y_log=False, output_path=out_dir / "throughput.svg")
    render_chart("Payload size vs. memory per op", "KiB/op (log)", memory, y_log=True, output_path=out_dir / "memory.svg")
    render_chart("Payload size vs. time per op", "ms/op (log)", time_, y_log=True, output_path=out_dir / "time.svg")
    print(f"BENCHMARK_CHARTS_OK output={out_dir}")


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""Generate the documentation diagrams (SVG) in this directory.

Report style: white background, outline-only boxes, no color fills, one
accent-free stroke color, so the diagrams print cleanly and read in light
and dark viewers alike. Run: python3 docs/diagrams/generate.py
"""
from pathlib import Path
from xml.sax.saxutils import escape

HERE = Path(__file__).parent
INK = "#1f2937"
MUTED = "#6b7280"
FONT = "font-family=\"-apple-system, 'Segoe UI', Helvetica, Arial, sans-serif\""


class SVG:
    def __init__(self, w, h, title):
        self.w, self.h, self.parts = w, h, []
        self.parts.append(f'<title>{escape(title)}</title>')
        self.parts.append(f'<rect width="{w}" height="{h}" fill="#ffffff"/>')

    def box(self, x, y, w, h, title, sub="", dashed=False, rx=6):
        dash = ' stroke-dasharray="6 4"' if dashed else ""
        self.parts.append(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{rx}" fill="none" stroke="{INK}" stroke-width="1.5"{dash}/>')
        cy = y + h / 2 + (-4 if sub else 5)
        self.parts.append(f'<text x="{x + w / 2}" y="{cy}" text-anchor="middle" font-size="14" font-weight="600" fill="{INK}" {FONT}>{escape(title)}</text>')
        if sub:
            self.parts.append(f'<text x="{x + w / 2}" y="{cy + 18}" text-anchor="middle" font-size="12" fill="{MUTED}" {FONT}>{escape(sub)}</text>')

    def group(self, x, y, w, h, label):
        self.parts.append(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="10" fill="none" stroke="{MUTED}" stroke-width="1" stroke-dasharray="4 4"/>')
        self.parts.append(f'<text x="{x + 10}" y="{y + 18}" font-size="12" fill="{MUTED}" {FONT}>{escape(label)}</text>')

    def arrow(self, points, label="", lx=None, ly=None, dashed=False, anchor="middle"):
        d = "M " + " L ".join(f"{px} {py}" for px, py in points)
        dash = ' stroke-dasharray="5 4"' if dashed else ""
        self.parts.append(f'<path d="{d}" fill="none" stroke="{INK}" stroke-width="1.3" marker-end="url(#arrow)"{dash}/>')
        if label:
            if lx is None:
                (x1, y1), (x2, y2) = points[0], points[-1]
                lx, ly = (x1 + x2) / 2, (y1 + y2) / 2 - 6
            self.parts.append(f'<text x="{lx}" y="{ly}" text-anchor="{anchor}" font-size="11.5" fill="{MUTED}" {FONT}>{escape(label)}</text>')

    def text(self, x, y, s, size=12, anchor="start", weight="400", color=MUTED):
        self.parts.append(f'<text x="{x}" y="{y}" text-anchor="{anchor}" font-size="{size}" font-weight="{weight}" fill="{color}" {FONT}>{escape(s)}</text>')

    def write(self, name):
        defs = (f'<defs><marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">'
                f'<path d="M 0 0 L 10 5 L 0 10 z" fill="{INK}"/></marker></defs>')
        svg = (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {self.w} {self.h}" width="{self.w}" height="{self.h}" role="img">'
               + defs + "".join(self.parts) + "</svg>\n")
        (HERE / name).write_text(svg)
        print("wrote", name)


def architecture():
    s = SVG(980, 500, "CloudDeploy architecture")
    s.group(220, 28, 460, 290, "Kubernetes namespace: clouddeploy")
    s.box(30, 60, 150, 54, "Browser", "dashboard user")
    s.box(240, 60, 170, 54, "Web (nginx)", "React dashboard, CSP")
    s.box(470, 60, 190, 54, "API (Go)", "REST · SSE · RBAC")
    s.box(240, 150, 170, 54, "Migrate job", "schema owner, hook")
    s.box(470, 230, 190, 54, "Worker (Go)", "SKIP LOCKED · build · deploy")
    s.box(750, 40, 200, 54, "Redis", "rate limits (GCRA)")
    s.box(750, 140, 200, 64, "PostgreSQL", "state · queue · audit")
    s.box(750, 250, 200, 60, "Image builds", "Docker (local) · ACR Tasks")
    s.box(470, 400, 190, 54, "Kubernetes API", "server-side apply")
    s.box(750, 390, 200, 70, "Tenant namespaces", "Deployment · Service · PSS")
    s.box(30, 400, 170, 60, "Observability", "Prometheus · Jaeger", dashed=True)

    s.arrow([(180, 87), (240, 87)])
    s.arrow([(410, 87), (470, 87)], "/api", 440, 80)
    s.arrow([(660, 75), (750, 67)], "limits", 705, 64)
    s.arrow([(660, 100), (750, 160)], "app role (DML)", 700, 113, anchor="start")
    s.arrow([(410, 177), (750, 177)], "DDL, app login", 590, 171)
    s.arrow([(660, 245), (750, 192)], "queue claim", 700, 203)
    s.arrow([(660, 268), (750, 280)], "build", 705, 284)
    s.arrow([(565, 284), (565, 400)], "apply + watch rollout", 575, 350, anchor="start")
    s.arrow([(470, 104), (445, 104), (445, 427), (470, 427)], "health · logs (read)", 437, 380, anchor="end")
    s.arrow([(660, 427), (750, 427)])
    s.arrow([(850, 310), (850, 390)], "image", 858, 355, anchor="start")
    s.arrow([(300, 318), (200, 400)], "metrics · traces", 215, 368, dashed=True)
    s.write("architecture.svg")


def lifecycle():
    s = SVG(980, 330, "Deployment lifecycle")
    s.text(30, 30, "Every transition is a compare-and-swap UPDATE; workers hold a lease renewed by heartbeats.", 12.5)
    xs = [30, 270, 510, 750]
    names = [("QUEUED", "API, idempotency key"), ("BUILDING", "worker claimed (lease)"),
             ("DEPLOYING", "image ready"), ("RUNNING", "rollout healthy")]
    for x, (n, sub) in zip(xs, names):
        s.box(x, 60, 200, 60, n, sub)
    for a, b in zip(xs, xs[1:]):
        s.arrow([(a + 200, 90), (b, 90)])
    s.box(270, 220, 200, 60, "FAILED", "error recorded, logs kept")
    s.box(510, 220, 200, 60, "CANCELLED", "user cancel, run stopped")
    # Failure (solid) into FAILED; cancellation (dashed) into CANCELLED.
    s.arrow([(150, 120), (310, 220)])
    s.arrow([(370, 120), (370, 220)])
    s.arrow([(560, 120), (430, 220)])
    s.arrow([(190, 120), (540, 220)], dashed=True)
    s.arrow([(420, 120), (580, 220)], dashed=True)
    s.arrow([(640, 120), (640, 220)], dashed=True)
    s.arrow([(270, 262), (110, 262), (110, 120)], "auto-rollback:", 118, 200, anchor="start")
    s.text(118, 215, "new deployment", 11.5)
    s.text(30, 320, "Solid: failure path (build error, failed rollout, lost lease).   Dashed: cancellation.", 12)
    s.write("lifecycle.svg")


def delivery():
    s = SVG(980, 300, "CI/CD pipeline")
    s.box(20, 110, 130, 60, "Push / tag", "main · v*.*.*")
    s.box(190, 60, 170, 160, "", "")
    s.text(275, 84, "CI", 14, anchor="middle", weight="600", color=INK)
    for i, line in enumerate(["lint · unit · integration", "k3s tests · dashboard", "Helm · Terraform (mocked)",
                              "image build + Trivy", "govulncheck · gitleaks", "CodeQL (separate)"]):
        s.text(275, 108 + i * 19, line, 11.5, anchor="middle")
    s.box(400, 110, 160, 60, "Publish", "GHCR · SBOM · cosign")
    s.box(600, 110, 160, 60, "End-to-end", "verify sig · k3s · API")
    s.box(800, 60, 160, 60, "Release", "tags: chart + notes")
    s.box(800, 170, 160, 60, "Deploy to AKS", "gated · approval · OIDC", dashed=True)
    s.arrow([(150, 140), (190, 140)])
    s.arrow([(360, 140), (400, 140)])
    s.arrow([(560, 140), (600, 140)])
    s.text(580, 128, "by digest", 11.5, anchor="middle")
    s.arrow([(760, 125), (800, 95)])
    s.arrow([(760, 155), (800, 195)], dashed=True)
    s.text(20, 280, "Nightly: all fuzz targets · integration ×3 under -race · k6 at 300 req/s with 300k deployments (p95 < 100 ms).", 12)
    s.write("delivery.svg")


if __name__ == "__main__":
    architecture()
    lifecycle()
    delivery()

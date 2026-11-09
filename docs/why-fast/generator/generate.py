"""Write the animated SVG diagrams of docs/why-fast.

Run: python3 docs/why-fast/generator/generate.py
"""
import os

OUT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

FONT = 'system-ui,-apple-system,"Segoe UI",Helvetica,Arial,sans-serif'
MONO = 'ui-monospace,SFMono-Regular,Menlo,Consolas,monospace'

LIGHT = dict(bg='#ffffff', border='#d1d9e0', text='#1f2328', muted='#59636e',
             line='#818b98', empty='#f6f8fa', old='#6e7781', new='#0969da',
             code='#9a6700', extra='#8250df', lit='#1a7f37', seq='#bc4c00',
             onfill='#ffffff', newsoft='#ddf4ff')
DARK = dict(bg='#0d1117', border='#3d444d', text='#f0f6fc', muted='#9198a1',
            line='#656c76', empty='#151b23', old='#8d96a0', new='#4493f8',
            code='#d29922', extra='#ab7df8', lit='#3fb950', seq='#f0883e',
            onfill='#0d1117', newsoft='#0c2d6b')


def palette_css(p):
    return f"""
.bg{{fill:{p['bg']};stroke:{p['border']}}}
.t{{fill:{p['text']}}}
.m{{fill:{p['muted']}}}
.ln{{stroke:{p['line']};fill:none}}
.lnf{{fill:{p['line']}}}
.cell{{fill:{p['empty']};stroke:{p['line']}}}
.f-old{{fill:{p['old']}}}
.f-new{{fill:{p['new']}}}
.f-code{{fill:{p['code']}}}
.f-extra{{fill:{p['extra']}}}
.f-lit{{fill:{p['lit']}}}
.f-seq{{fill:{p['seq']}}}
.f-soft{{fill:{p['newsoft']};stroke:{p['new']}}}
.s-new{{stroke:{p['new']};fill:none}}
.s-code{{stroke:{p['code']};fill:none}}
.on{{fill:{p['onfill']}}}
.cur{{stroke:{p['text']}}}
"""


class Svg:
    def __init__(self, w, h, title, desc, panel=None):
        self.w, self.h = w, h
        self.panel = panel
        self.title, self.desc = title, desc
        self.body = []
        self.css = []
        self.n = 0

    def add(self, s):
        self.body.append(s)

    def anim(self, frames, dur, extra=''):
        self.n += 1
        name = f'k{self.n}'
        self.css.append(f'@keyframes {name}{{{frames}}}')
        return f'animation:{name} {dur}s linear infinite;{extra}'

    def render(self, path):
        css = (f'text{{font-family:{FONT};font-size:13px}}'
               f'.mono{{font-family:{MONO}}}'
               '.b{font-weight:600}.h{font-size:16px;font-weight:600}'
               '.s12{font-size:12px}.s11{font-size:11px}'
               '.grow{transform-box:fill-box;transform-origin:left center}'
               + palette_css(LIGHT)
               + '@media (prefers-color-scheme:dark){' + palette_css(DARK) + '}'
               + '\n'.join(self.css)
               + '@media (prefers-reduced-motion:reduce){*{animation:none!important}}')
        out = (f'<svg xmlns="http://www.w3.org/2000/svg" width="{self.w}" height="{self.h}" viewBox="0 0 {self.w} {self.h}" '
               f'role="img" aria-labelledby="title desc">'
               f'<title id="title">{self.title}</title><desc id="desc">{self.desc}</desc>'
               f'<style>{css}</style>'
               + (self.panel or f'<rect class="bg" x="0.5" y="0.5" width="{self.w-1}" height="{self.h-1}" rx="12"/>')
               + ''.join(self.body) + '</svg>\n')
        with open(path, 'w', encoding='utf-8', newline='\n') as f:
            f.write(out)


def pct(t, T):
    return f'{max(0.0, min(100.0, t / T * 100)):.2f}%'


def appear(t, T, d=0.3, hidden='opacity:0', shown='opacity:1', end=None):
    """Hidden until t, shown from t+d until the loop fades everything at the end."""
    end = T - 0.5 if end is None else end
    return (f'0%,{pct(t, T)}{{{hidden}}}'
            f'{pct(t + d, T)},{pct(end, T)}{{{shown}}}'
            f'{pct(end + 0.3, T)},100%{{opacity:0}}')


def label_block(s, x, y, head, sub):
    s.add(f'<text class="t b" x="{x}" y="{y}">{head}</text>')
    s.add(f'<text class="m" x="{x}" y="{y + 20}">{sub}</text>')


# 1. Refill -----------------------------------------------------------------

def refill():
    T = 6.0
    s = Svg(880, 290, 'Refill the bit buffer',
            'compress/gzip and klauspost refill a 32-bit buffer one byte for each turn of a loop. '
            'fastcompress refills a 64-bit buffer with one unaligned 8-byte load and no branch.')
    s.add('<text class="t h" x="28" y="40">Refill the bit buffer</text>')
    bytes_ = ['1f', '8b', '08', '00', 'a3', '5c', '12', '64']
    cw, ch, x0 = 50, 40, 300

    def row(y, n, times, head, sub):
        label_block(s, 28, y + 18, head, sub)
        for i in range(8 if n == 8 else 4):
            s.add(f'<rect class="cell" x="{x0 + i * cw}" y="{y}" width="{cw - 4}" height="{ch}" rx="4"/>')
        fill = 'f-new' if n == 8 else 'f-old'
        for i in range(n):
            t = times[i]
            st = s.anim(appear(t, T, 0.3, 'opacity:0;transform:translateY(-26px)',
                               'opacity:1;transform:translateY(0)'), T)
            s.add(f'<g style="{st}"><rect class="{fill}" x="{x0 + i * cw}" y="{y}" '
                  f'width="{cw - 4}" height="{ch}" rx="4"/>'
                  f'<text class="on mono" x="{x0 + i * cw + (cw - 4) / 2}" y="{y + 25}" '
                  f'text-anchor="middle">{bytes_[i]}</text></g>')
        bw = (8 if n == 8 else 4) * cw - 4
        s.add(f'<path class="ln" d="M{x0} {y + ch + 8}v6h{bw}v-6"/>')
        s.add(f'<text class="m s12" x="{x0 + bw / 2}" y="{y + ch + 30}" text-anchor="middle">'
              f'{"64" if n == 8 else "32"} bits</text>')

    old_t = [0.6, 1.5, 2.4, 3.3]
    row(70, 4, old_t, 'compress/gzip, klauspost', '32-bit buffer, 1 byte per turn')
    row(180, 8, [0.6] * 8, 'fastcompress', '64-bit buffer, one 8-byte load')

    # Turn counter for the byte loop: one number for each byte that drops in.
    for k, t in enumerate(old_t):
        nxt = old_t[k + 1] if k + 1 < len(old_t) else None
        if nxt is None:
            st = s.anim(appear(t, T, 0.05), T)
        else:
            st = s.anim(f'0%,{pct(t, T)}{{opacity:0}}{pct(t + 0.05, T)},{pct(nxt, T)}{{opacity:1}}'
                        f'{pct(nxt + 0.05, T)},100%{{opacity:0}}', T)
        base = '' if nxt is None else 'opacity:0;'
        s.add(f'<text class="t b" x="530" y="96" style="{base}{st}">turn {k + 1}</text>')
    s.add('<text class="m s12" x="530" y="114">a branch on each turn</text>')
    st = s.anim(appear(0.9, T, 0.2), T)
    s.add(f'<text class="t b" x="712" y="206" style="{st}">1 load</text>')
    s.add(f'<text class="m s12" x="712" y="224" style="{st}">no branch</text>')
    s.render(os.path.join(OUT, 'refill.svg'))


# 2. One shift --------------------------------------------------------------

def shift():
    T = 7.0
    s = Svg(880, 310, 'Remove a code and its extra bits',
            'compress/gzip and klauspost shift out the code, then read the extra bits a second time '
            'and shift again. In fastcompress the table entry holds the code length plus the extra '
            'bits, so one shift removes both.')
    s.add('<text class="t h" x="28" y="40">Remove a code and its extra bits</text>')
    # Legend
    s.add('<rect class="f-code" x="590" y="28" width="14" height="14" rx="3"/>'
          '<text class="m s12" x="610" y="40">code</text>'
          '<rect class="f-extra" x="660" y="28" width="14" height="14" rx="3"/>'
          '<text class="m s12" x="680" y="40">extra bits</text>'
          '<rect class="cell" x="756" y="28" width="14" height="14" rx="3"/>'
          '<text class="m s12" x="776" y="40">next bits</text>')
    bits = '1011001' + '110' + '01101001110100'
    cw, x0 = 22, 300
    ncode, nextra = 7, 3

    def strip(y, cells, cls):
        out = []
        for i, b in cells:
            if cls == 'cell':
                out.append(f'<rect class="cell" x="{x0 + i * cw}" y="{y}" width="{cw - 3}" height="30" rx="3"/>'
                           f'<text class="t mono s12" x="{x0 + i * cw + (cw - 3) / 2}" y="{y + 20}" '
                           f'text-anchor="middle">{b}</text>')
            else:
                out.append(f'<rect class="{cls}" x="{x0 + i * cw}" y="{y}" width="{cw - 3}" height="30" rx="3"/>'
                           f'<text class="on mono s12" x="{x0 + i * cw + (cw - 3) / 2}" y="{y + 20}" '
                           f'text-anchor="middle">{b}</text>')
        return ''.join(out)

    code = list(enumerate(bits[:ncode]))
    extra = [(i, bits[i]) for i in range(ncode, ncode + nextra)]
    rest = [(i, bits[i]) for i in range(ncode + nextra, len(bits))]

    def fade(t):
        return s.anim(f'0%,{pct(t, T)}{{opacity:1}}{pct(t + 0.3, T)},{pct(T - 0.6, T)}{{opacity:0}}'
                      f'{pct(T - 0.3, T)},100%{{opacity:1}}', T)

    def move(steps):
        frames = '0%{transform:translateX(0)}'
        cur = 0
        for t, dx in steps:
            frames += f'{pct(t, T)}{{transform:translateX({cur}px)}}'
            cur = dx
            frames += f'{pct(t + 0.3, T)}{{transform:translateX({cur}px)}}'
        frames += f'{pct(T - 0.6, T)}{{transform:translateX({cur}px);opacity:1}}'
        frames += f'{pct(T - 0.35, T)}{{transform:translateX({cur}px);opacity:0}}'
        frames += f'{pct(T - 0.3, T)}{{transform:translateX(0);opacity:0}}100%{{transform:translateX(0);opacity:1}}'
        return s.anim(frames, T)

    def step_label(x, y, text, t_on, t_off):
        st = s.anim(f'0%,{pct(t_on, T)}{{opacity:.35}}{pct(t_on + 0.2, T)},{pct(t_off, T)}{{opacity:1}}'
                    f'{pct(t_off + 0.2, T)},100%{{opacity:.35}}', T)
        s.add(f'<text class="t s12" x="{x}" y="{y}" style="{st}">{text}</text>')

    # compress/gzip and klauspost: two reads, two shifts.
    y = 74
    label_block(s, 28, y + 12, 'compress/gzip, klauspost', 'extra bits need a second read')
    t1, t2 = 1.5, 3.6
    s.add(f'<g style="{fade(t1)}">{strip(y, code, "f-code")}</g>')
    s.add(f'<g style="{move([(t1, -ncode * cw)])}"><g style="{fade(t2)}">{strip(y, extra, "f-extra")}</g></g>')
    s.add(f'<g style="{move([(t1, -ncode * cw), (t2, -(ncode + nextra) * cw)])}">{strip(y, rest, "cell")}</g>')
    step_label(x0, y + 54, '1. look up the code, shift 7', 0.8, 2.3)
    step_label(x0 + 210, y + 54, '2. read 3 extra bits, shift 3', 2.9, 4.4)

    # fastcompress: one entry, one shift.
    y = 186
    label_block(s, 28, y + 12, 'fastcompress', 'the entry holds the extra bits')
    s.add(f'<g style="{fade(t1)}">{strip(y, code, "f-code")}{strip(y, extra, "f-extra")}</g>')
    s.add(f'<g style="{move([(t1, -(ncode + nextra) * cw)])}">{strip(y, rest, "cell")}</g>')
    step_label(x0, y + 54, '1. look up the entry, shift 7 + 3', 0.8, 2.3)
    s.add(f'<text class="m s12" x="{x0}" y="{y + 76}">The next symbol starts here, one step earlier.</text>')
    s.render(os.path.join(OUT, 'shift.svg'))


# 3. Match copy -------------------------------------------------------------

def copy():
    T = 7.0
    s = Svg(880, 330, 'Copy a match',
            'compress/gzip and klauspost copy a match into a 32 KB ring buffer with copy calls, and a '
            'match can wrap at the end. fastcompress keeps the last 32 KB plus up to 1 MB of new output '
            'in a flat buffer, copies 16 bytes at a time, and a match never wraps.')
    s.add('<text class="t h" x="28" y="40">Copy a match</text>')
    s.add('<rect class="f-code" x="640" y="28" width="14" height="14" rx="3"/>'
          '<text class="m s12" x="660" y="40">source</text>'
          '<rect class="f-extra" x="720" y="28" width="14" height="14" rx="3"/>'
          '<text class="m s12" x="740" y="40">copied match</text>')
    x0, x1, h = 300, 840, 34

    # Ring buffer.
    y = 84
    label_block(s, 28, y + 12, 'compress/gzip, klauspost', '32 KB ring buffer, copy calls')
    s.add(f'<rect class="cell" x="{x0}" y="{y}" width="{x1 - x0}" height="{h}" rx="4"/>')
    wpos = 770
    s.add(f'<rect class="f-old" x="{x0}" y="{y}" width="{wpos - x0}" height="{h}" rx="4" opacity=".35"/>')
    s.add(f'<rect class="f-code" x="640" y="{y}" width="100" height="{h}"/>')
    a1 = s.anim(appear(0.8, T, 0.6, 'opacity:0;transform:scaleX(0)', 'opacity:1;transform:scaleX(1)'), T, '')
    s.add(f'<rect class="f-extra grow" style="{a1}" x="{wpos}" y="{y}" width="{x1 - wpos}" height="{h}"/>')
    a2 = s.anim(appear(2.6, T, 0.4, 'opacity:0;transform:scaleX(0)', 'opacity:1;transform:scaleX(1)'), T, '')
    s.add(f'<rect class="f-extra grow" style="{a2}" x="{x0}" y="{y}" width="30" height="{h}"/>')
    l1 = s.anim(appear(0.8, T, 0.3), T)
    s.add(f'<text class="t s12" x="{(wpos + x1) / 2}" y="{y + h + 18}" text-anchor="middle" style="{l1}">copy 1</text>')
    w = s.anim(appear(2.0, T, 0.4), T)
    s.add(f'<path class="ln" style="{w}" d="M{x1 - 4} {y - 4}C{x1 - 40} {y - 30} {x0 + 60} {y - 30} {x0 + 14} {y - 6}" '
          'stroke-dasharray="4 4"/>')
    s.add(f'<path class="lnf" style="{w}" d="M{x0 + 10} {y - 4}l2 -10l7 7z"/>')
    s.add(f'<text class="m s12" x="{(x0 + x1) / 2}" y="{y - 10}" text-anchor="middle" style="{w}">'
          'end of the buffer: wrap to the start</text>')
    l2 = s.anim(appear(2.6, T, 0.3), T)
    s.add(f'<text class="t s12" x="{x0 + 15}" y="{y + h + 18}" text-anchor="middle" style="{l2}">copy 2</text>')

    # Flat buffer.
    y = 214
    label_block(s, 28, y + 12, 'fastcompress', 'flat buffer, 16 bytes at a time')
    hist = 440
    s.add(f'<rect class="f-old" x="{x0}" y="{y}" width="{hist - x0}" height="{h}" rx="4" opacity=".35"/>')
    s.add(f'<rect class="f-soft" x="{hist}" y="{y}" width="{x1 - hist}" height="{h}" rx="4"/>')
    s.add(f'<text class="m s12" x="{(x0 + hist) / 2}" y="{y + h + 18}" text-anchor="middle">last 32 KB</text>')
    s.add(f'<text class="m s12" x="{(hist + x1) / 2 + 60}" y="{y + h + 18}" text-anchor="middle">'
          'new output, up to 1 MB</text>')
    wpos = 600
    s.add(f'<rect class="f-new" x="{hist}" y="{y}" width="{wpos - hist}" height="{h}" opacity=".35"/>')
    s.add(f'<rect class="f-code" x="490" y="{y}" width="100" height="{h}"/>')
    for k in range(5):
        t = 0.8 + k * 0.22
        st = s.anim(appear(t, T, 0.12), T)
        s.add(f'<rect class="f-extra" style="{st}" x="{wpos + k * 20}" y="{y}" width="19" height="{h}"/>')
    st = s.anim(appear(0.8, T, 0.2), T)
    s.add(f'<path class="ln" style="{st}" d="M{wpos} {y - 6}v-6h19v6"/>'
          f'<text class="t s12" style="{st}" x="{wpos + 10}" y="{y - 18}" text-anchor="middle">16 B</text>')
    st = s.anim(appear(2.2, T, 0.3), T)
    s.add(f'<text class="t s12" style="{st}" x="{wpos + 112}" y="{y + 22}">a match never wraps</text>')
    s.render(os.path.join(OUT, 'copy.svg'))


# Timelines -----------------------------------------------------------------

def timeline(name, title, desc, groups, unit, note, legend=()):
    """groups: [(head, sub, [(lane label, [(start, length, cls, text)])])]"""
    x0, lane_h, gap = 300, 26, 8
    rows = sum(len(g[2]) for g in groups)
    h = 70 + len(groups) * 44 + rows * (lane_h + gap) + 34
    end_units = max(st + ln for g in groups for _, segs in g[2] for st, ln, _, _ in segs)
    speed = 0.38
    T = end_units * speed + 2.6
    s = Svg(880, h, title, desc)
    s.add(f'<text class="t h" x="28" y="40">{title}</text>')
    lx = 880 - 28
    for cls, text in reversed(legend):
        w = 7.0 * len(text) + 26
        lx -= w
        s.add(f'<rect class="{cls}" x="{lx}" y="28" width="14" height="14" rx="3"/>'
              f'<text class="m s12" x="{lx + 20}" y="40">{text}</text>')
    y = 70
    top = y
    for head, sub, lanes in groups:
        s.add(f'<text class="t b" x="28" y="{y + 14}">{head}</text>')
        if sub:
            s.add(f'<text class="m s12" x="{28 + 7.6 * len(head) + 12}" y="{y + 14}">{sub}</text>')
        y += 26
        gend = max(st + ln for _, segs in lanes for st, ln, _, _ in segs)
        for lane, segs in lanes:
            s.add(f'<text class="m s12" x="44" y="{y + 17}">{lane}</text>')
            s.add(f'<rect class="cell" x="{x0}" y="{y}" width="{end_units * unit}" height="{lane_h}" rx="4" '
                  'opacity=".6"/>')
            for st_, ln, cls, text in segs:
                a = s.anim(appear(st_ * speed, T, ln * speed, 'opacity:1;transform:scaleX(0)',
                                  'opacity:1;transform:scaleX(1)'), T)
                sx = x0 + st_ * unit
                s.add(f'<rect class="{cls} grow" style="{a}" x="{sx + 1}" y="{y + 1}" '
                      f'width="{ln * unit - 2}" height="{lane_h - 2}" rx="3"/>')
                ta = s.anim(appear((st_ + ln) * speed, T, 0.15), T)
                if text.startswith('>'):
                    s.add(f'<text class="t s12 mono" style="{ta}" x="{sx + ln * unit + 8}" y="{y + 17}">{text[1:]}</text>')
                elif text:
                    s.add(f'<text class="on s12" style="{ta}" x="{sx + ln * unit / 2}" y="{y + 17}" '
                          f'text-anchor="middle">{text}</text>')
            y += lane_h + gap
        ex = x0 + gend * unit
        d = s.anim(appear(gend * speed, T, 0.2), T)
        s.add(f'<path class="ln" style="{d}" d="M{ex} {y - (lane_h + gap) * len(lanes) - 4}V{y - gap + 4}" '
              'stroke-width="2"/>')
        y += 18
    bottom = y - 18
    cur = s.anim(f'0%{{transform:translateX(0);opacity:1}}{pct(end_units * speed, T)}'
                 f'{{transform:translateX({end_units * unit}px);opacity:1}}'
                 f'{pct(T - 0.5, T)}{{transform:translateX({end_units * unit}px);opacity:1}}'
                 f'{pct(T - 0.2, T)},100%{{transform:translateX({end_units * unit}px);opacity:0}}', T)
    s.add(f'<path class="cur" style="opacity:0;{cur}" d="M{x0} {top}V{bottom}" stroke-width="1" '
          'stroke-dasharray="3 3"/>')
    s.add(f'<text class="m s12" x="28" y="{h - 20}">{note}</text>')
    s.render(os.path.join(OUT, name))


def gunzip_crc():
    D, C = 4, 1
    old = []
    for k in range(3):
        old += [(k * (D + C), D, 'f-old', 'decode'), (k * (D + C) + D, C, 'f-extra', 'CRC')]
    new_d = [(k * D, D, 'f-new', 'decode ≥1 MB') for k in range(3)]
    new_c = [((k + 1) * D, C, 'f-extra', 'CRC') for k in range(3)]
    timeline('gunzip-crc.svg', 'Compute CRC-32 on a second goroutine',
             'compress/gzip decodes and computes CRC-32 on the same goroutine. Above 1 MB, Gunzip gives '
             'the output to a second goroutine in steps of 1 MB or more, and that goroutine computes '
             'the CRC-32 while the decoder continues.',
             [('compress/gzip, klauspost', 'one goroutine', [('goroutine 1', old)]),
              ('fastcompress Gunzip', 'output above 1 MB', [('goroutine 1', new_d), ('goroutine 2', new_c)])],
             unit=30, note='The lengths of the bars are an example, not a measurement.')


def gzip_concurrent():
    C, P = 4, 0.6
    old = [(k * C, C, 'f-old', f'chunk {k + 1}') for k in range(4)]
    lanes = [(f'goroutine {k + 1}', [(0, P, 'f-code', ''), (P, C, 'f-new', f'chunk {k + 1}')]) for k in range(4)]
    lanes.append(('join', [(P + C, 1, 'f-extra', '>crc32_combine')]))
    timeline('gzip-concurrent.svg', 'Compress chunks on more than one goroutine',
             'compress/gzip compresses a stream on one goroutine. GzipConcurrent splits the input into '
             'chunks of 16 blocks. Each goroutine first puts the 32 KB before its chunk in the hash '
             'tables, compresses the chunk and computes its CRC-32. crc32_combine joins the checksums.',
             [('compress/gzip, klauspost', 'one goroutine', [('goroutine 1', old)]),
              ('fastcompress GzipConcurrent', 'chunks of 16 blocks', lanes)],
             unit=30, note='The number of goroutines and the lengths of the bars are an example.',
             legend=(('f-code', '32 KB before the chunk'), ('f-new', 'compress + CRC-32')))


def zstd_pipeline():
    L, S, E = 1.0, 1.5, 2.0
    old = []
    t = 0
    for k in range(3):
        old += [(t, L, 'f-lit', f'L{k + 1}'), (t + L, S, 'f-seq', f'S{k + 1}'), (t + L + S, E, 'f-new', f'E{k + 1}')]
        t += L + S + E
    amd_a, amd_b = [], []
    e_end = 0
    for k in range(3):
        st = k * (L + S)
        amd_a += [(st, L, 'f-lit', f'L{k + 1}'), (st + L, S, 'f-seq', f'S{k + 1}')]
        es = max(st + L + S, e_end)
        amd_b.append((es, E, 'f-new', f'E{k + 1}'))
        e_end = es + E
    arm_l = [(k * L, L, 'f-lit', f'L{k + 1}') for k in range(3)]
    arm_s = [(k * S, S, 'f-seq', f'S{k + 1}') for k in range(3)]
    arm_e = []
    e_end = 0
    for k in range(3):
        es = max((k + 1) * L, (k + 1) * S, e_end)
        arm_e.append((es, E, 'f-new', f'E{k + 1}'))
        e_end = es + E
    timeline('zstd-pipeline.svg', 'Decode a large zstd frame',
             'klauspost DecodeAll decodes the literals and the sequences of each block and executes them '
             'on one goroutine. For a frame of 1 MB or more, zstd.Decode uses two goroutines on amd64 and '
             'three on arm64, so the next block decodes while the current block executes.',
             [('klauspost DecodeAll', 'one goroutine', [('goroutine 1', old)]),
              ('fastcompress zstd.Decode, amd64', 'frame of 1 MB or more',
               [('literals + sequences', amd_a), ('execute', amd_b)]),
              ('fastcompress zstd.Decode, arm64', 'frame of 1 MB or more',
               [('literals', arm_l), ('sequences', arm_s), ('execute', arm_e)])],
             unit=38, note='L, S, E: literals, sequences and execution of block 1, 2, 3. The lengths are an example.',
             legend=(('f-lit', 'literals'), ('f-seq', 'sequences'), ('f-new', 'execute')))


# Overview ------------------------------------------------------------------

def overview():
    # Times are the "Log, level 1" row of the gzip decompression table in the README.
    runs = [('fastcompress', 67.5, '67.5 ms'),
            ('klauspost', 223, '223 ms'),
            ('compress/gzip', 310, '310 ms')]
    line = '2026-11-07T09:14:03.218Z INFO api GET /v1/orders/48213 200 12.4ms'
    ms_to_s, lead, hold = 0.012, 0.5, 2.6
    T = lead + max(ms for _, ms, _ in runs) * ms_to_s + hold
    w, h = 880, 272
    s = Svg(w, h, 'fastcompress decode time',
            'Three decoders write the same line of a 100 MB application log, each at the speed it measured '
            'on an Apple M1 Pro at gzip level 1: fastcompress in 67.5 ms, klauspost/compress in 223 ms and '
            'compress/gzip in 310 ms. fastcompress is about 3.3 times faster than klauspost. The decoder '
            'refills a 64-bit buffer with one 8-byte load, removes a code and its extra bits with one shift, '
            'copies matches 16 bytes at a time, and uses BMI2 and PMULL assembly.',
            panel=f'<rect class="paper" width="{w}" height="{h}" rx="6"/>')
    s.css.append('.paper{fill:#f7f6f2}.ink{fill:#1c1b19}.dim{fill:#6b6862}.acc{fill:#b8361e}'
                 '.rule{stroke:#d9d6ce}.big{font-size:22px;font-weight:700}.lg{font-size:14px}'
                 '.wipe{transform-box:fill-box;transform-origin:right center}'
                 '.num{font-variant-numeric:tabular-nums}'
                 '@media (prefers-color-scheme:dark){.paper{fill:#141414}.ink{fill:#ecebe6}'
                 '.dim{fill:#9b978f}.acc{fill:#ff7b5c}.rule{stroke:#2e2d2a}}')
    s.add('<text class="mono dim" x="28" y="38">gunzip, 100 MB application log, level 1, Apple M1 Pro</text>')
    x0, tx = 172, 724
    cw = 7.82
    lw = len(line) * cw + 4
    y = 82
    for name, ms, label in runs:
        main = name == 'fastcompress'
        dur = ms * ms_to_s
        s.add(f'<text class="mono lg {"ink" if main else "dim"}" x="28" y="{y}">{name}</text>')
        s.add(f'<text class="mono ink" x="{x0}" y="{y}">{line}</text>')
        cover = s.anim(f'0%,{pct(lead, T)}{{opacity:1;transform:scaleX(1)}}'
                       f'{pct(lead + dur, T)}{{opacity:1;transform:scaleX(0)}}'
                       f'{pct(T - 0.5, T)}{{opacity:1;transform:scaleX(0)}}'
                       f'{pct(T - 0.2, T)},100%{{opacity:1;transform:scaleX(1)}}', T)
        s.add(f'<rect class="paper wipe" style="opacity:0;{cover}" x="{x0 - 2}" y="{y - 15}" '
              f'width="{lw:.1f}" height="21"/>')
        caret = s.anim(f'0%,{pct(lead, T)}{{opacity:1;transform:translateX(0)}}'
                       f'{pct(lead + dur, T)}{{opacity:1;transform:translateX({lw:.1f}px)}}'
                       f'{pct(lead + dur + 0.25, T)},100%{{opacity:0;transform:translateX({lw:.1f}px)}}', T)
        s.add(f'<rect class="{"acc" if main else "dim"}" style="opacity:0;{caret}" x="{x0}" y="{y - 13}" '
              'width="8" height="17"/>')
        shown = s.anim(appear(lead + dur, T, 0.1), T)
        if main:
            s.add(f'<text class="mono num big acc" style="{shown}" x="{tx}" y="{y + 3}">{label}</text>')
        else:
            s.add(f'<text class="mono num lg dim" style="{shown}" x="{tx}" y="{y}">{label}</text>')
        y += 44
    s.add(f'<path class="rule" d="M28 {y - 10}H{w - 28}"/>')
    s.add(f'<text class="mono ink" x="28" y="{y + 18}">about 3.3× faster than klauspost/compress</text>')
    s.add(f'<text class="mono dim" x="28" y="{y + 40}">64-bit refill, one shift per code, 16-byte copies, '
          'BMI2 and PMULL assembly</text>')
    s.render(os.path.join(OUT, 'overview.svg'))

overview()
refill()
shift()
copy()
gunzip_crc()
gzip_concurrent()
zstd_pipeline()

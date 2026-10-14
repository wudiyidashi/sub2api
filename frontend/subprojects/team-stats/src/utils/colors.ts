/**
 * 分组配色工具：
 * - spreadColors: 展示层自动避让——按顺序保留原色，撞色的往后旋转色相直到可分辨
 * - evenPalette: 一键配色——按分组数量在色环上均匀取色
 */

function hexToRgb(hex: string): [number, number, number] {
  const s = hex.replace('#', '');
  const v = s.length === 3 ? s.split('').map((c) => c + c).join('') : s;
  const n = parseInt(v, 16);
  if (Number.isNaN(n) || v.length !== 6) return [0, 102, 204];
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
}

function rgbToHex(r: number, g: number, b: number): string {
  const c = (x: number) => Math.max(0, Math.min(255, Math.round(x))).toString(16).padStart(2, '0');
  return `#${c(r)}${c(g)}${c(b)}`;
}

function rgbToHsl(r: number, g: number, b: number): [number, number, number] {
  r /= 255; g /= 255; b /= 255;
  const max = Math.max(r, g, b);
  const min = Math.min(r, g, b);
  const l = (max + min) / 2;
  if (max === min) return [0, 0, l];
  const d = max - min;
  const s = l > 0.5 ? d / (2 - max - min) : d / (max + min);
  let h: number;
  if (max === r) h = ((g - b) / d + (g < b ? 6 : 0)) / 6;
  else if (max === g) h = ((b - r) / d + 2) / 6;
  else h = ((r - g) / d + 4) / 6;
  return [h * 360, s, l];
}

function hslToHex(h: number, s: number, l: number): string {
  h = ((h % 360) + 360) % 360 / 360;
  if (s === 0) {
    const v = l * 255;
    return rgbToHex(v, v, v);
  }
  const q = l < 0.5 ? l * (1 + s) : l + s - l * s;
  const p = 2 * l - q;
  const f = (t: number) => {
    t = ((t % 1) + 1) % 1;
    if (t < 1 / 6) return p + (q - p) * 6 * t;
    if (t < 1 / 2) return q;
    if (t < 2 / 3) return p + (q - p) * (2 / 3 - t) * 6;
    return p;
  };
  return rgbToHex(f(h + 1 / 3) * 255, f(h) * 255, f(h - 1 / 3) * 255);
}

/** redmean 加权 RGB 距离，近似人眼感知（0 ~ 约 765） */
export function colorDistance(hexA: string, hexB: string): number {
  const [r1, g1, b1] = hexToRgb(hexA);
  const [r2, g2, b2] = hexToRgb(hexB);
  const rMean = (r1 + r2) / 2;
  const dr = r1 - r2;
  const dg = g1 - g2;
  const db = b1 - b2;
  return Math.sqrt(
    (2 + rMean / 256) * dr * dr + 4 * dg * dg + (2 + (255 - rMean) / 256) * db * db,
  );
}

const DISTINCT_THRESHOLD = 125;
const GOLDEN_ANGLE = 137.508;

/**
 * 按输入顺序输出"可分辨"的颜色：与已接受颜色距离不足时，
 * 旋转色相（必要时再抬/压亮度）直到拉开差距。输入不撞色时原样返回。
 */
export function spreadColors(colors: string[]): string[] {
  const accepted: string[] = [];
  for (const raw of colors) {
    let candidate = raw || '#0066cc';
    const hsl = rgbToHsl(...hexToRgb(candidate));
    let [h, s] = hsl;
    const l = hsl[2];
    // 灰色系饱和度太低时旋转色相无效，先补一点饱和度再参与避让
    if (s < 0.12) s = 0.45;
    let tries = 0;
    while (accepted.some((c) => colorDistance(c, candidate) < DISTINCT_THRESHOLD) && tries < 24) {
      tries++;
      h += GOLDEN_ANGLE;
      const lAdj = tries > 8 ? (tries % 2 === 0 ? Math.min(0.68, l + 0.1) : Math.max(0.38, l - 0.1)) : l;
      candidate = hslToHex(h, Math.max(s, 0.45), lAdj);
    }
    accepted.push(candidate);
  }
  return accepted;
}

/** 在色环上均匀取 n 个颜色（从蓝色系起步，饱和度/亮度交替避免相邻过近）。 */
export function evenPalette(n: number): string[] {
  const out: string[] = [];
  for (let i = 0; i < n; i++) {
    const h = (210 + (i * 360) / Math.max(n, 1)) % 360;
    const l = n > 8 && i % 2 === 1 ? 0.62 : 0.52;
    out.push(hslToHex(h, 0.68, l));
  }
  return out;
}

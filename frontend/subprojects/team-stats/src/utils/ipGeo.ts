/**
 * IP 归属地解析：与 sub2api 主后台前端同源方案（get.geojs.io 批量接口）。
 * - HTTPS、免费、无需 key，单次最多 50 个 IP
 * - localStorage 缓存 24h，避免重复外呼
 * - 内网 IP 本地识别，不外发
 */

export interface IpGeo {
  ip: string;
  /** 展示用标签，如 "CN · Guangdong · Shenzhen"；内网为 "内网 IP"，失败为 "未知" */
  label: string;
  countryCode?: string;
  region?: string;
  city?: string;
  organization?: string;
  latitude?: number;
  longitude?: number;
  isPrivate: boolean;
}

const CACHE_KEY = 'subStats:ipGeoCache:v1';
const CACHE_TTL_MS = 24 * 60 * 60 * 1000;
const BATCH_URL = 'https://get.geojs.io/v1/ip/geo.json';
const BATCH_CHUNK = 50;

interface StoredEntry {
  geo: IpGeo;
  fetchedAt: number;
}

export function isPrivateIp(ip: string): boolean {
  const v4 = ip.match(/^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/);
  if (v4) {
    const a = Number(v4[1]);
    const b = Number(v4[2]);
    if (a === 10 || a === 127) return true;
    if (a === 169 && b === 254) return true;
    if (a === 172 && b >= 16 && b <= 31) return true;
    if (a === 192 && b === 168) return true;
    return false;
  }
  const lower = ip.toLowerCase();
  if (lower === '::1') return true;
  const first = lower.split(':', 1)[0];
  if (/^fe[89ab][0-9a-f]$/.test(first)) return true;
  if (/^f[cd][0-9a-f]{2}$/.test(first)) return true;
  return false;
}

function loadCache(): Record<string, StoredEntry> {
  try {
    const raw = localStorage.getItem(CACHE_KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw) as Record<string, StoredEntry>;
    const now = Date.now();
    const out: Record<string, StoredEntry> = {};
    for (const [ip, e] of Object.entries(parsed)) {
      if (e && typeof e.fetchedAt === 'number' && now - e.fetchedAt <= CACHE_TTL_MS) {
        out[ip] = e;
      }
    }
    return out;
  } catch {
    return {};
  }
}

function saveCache(cache: Record<string, StoredEntry>) {
  try {
    localStorage.setItem(CACHE_KEY, JSON.stringify(cache));
  } catch {
    /* 隐私模式等场景忽略 */
  }
}

interface RawGeoResponse {
  ip: string;
  country_code?: string;
  region?: string;
  city?: string;
  organization?: string;
  latitude?: string;
  longitude?: string;
}

function toGeo(ip: string, raw: RawGeoResponse | undefined): IpGeo {
  if (!raw || !raw.country_code) {
    return { ip, label: '未知', isPrivate: false };
  }
  const parts = [raw.country_code, raw.region, raw.city].filter(
    (p): p is string => Boolean(p && p.trim()),
  );
  const lat = raw.latitude !== undefined ? parseFloat(raw.latitude) : NaN;
  const lon = raw.longitude !== undefined ? parseFloat(raw.longitude) : NaN;
  return {
    ip,
    label: parts.join(' · ') || raw.country_code,
    countryCode: raw.country_code,
    region: raw.region,
    city: raw.city,
    organization: raw.organization,
    latitude: Number.isFinite(lat) ? lat : undefined,
    longitude: Number.isFinite(lon) ? lon : undefined,
    isPrivate: false,
  };
}

/** 批量解析 IP 归属地；结果含缓存命中项，key 为原始 IP。 */
export async function resolveIpGeos(ips: string[]): Promise<Map<string, IpGeo>> {
  const unique = Array.from(new Set(ips.filter(Boolean)));
  const result = new Map<string, IpGeo>();
  const cache = loadCache();
  const targets: string[] = [];

  for (const ip of unique) {
    if (isPrivateIp(ip)) {
      result.set(ip, { ip, label: '内网 IP', isPrivate: true });
    } else if (cache[ip]) {
      result.set(ip, cache[ip].geo);
    } else {
      targets.push(ip);
    }
  }

  for (let i = 0; i < targets.length; i += BATCH_CHUNK) {
    const chunk = targets.slice(i, i + BATCH_CHUNK);
    try {
      const res = await fetch(`${BATCH_URL}?ip=${chunk.map(encodeURIComponent).join(',')}`);
      if (!res.ok) {
        chunk.forEach((ip) => result.set(ip, { ip, label: '未知', isPrivate: false }));
        continue;
      }
      const list = (await res.json()) as RawGeoResponse[];
      const byIp = new Map(list.map((r) => [r.ip, r]));
      const now = Date.now();
      for (const ip of chunk) {
        const geo = toGeo(ip, byIp.get(ip));
        result.set(ip, geo);
        if (geo.countryCode) {
          cache[ip] = { geo, fetchedAt: now };
        }
      }
    } catch {
      chunk.forEach((ip) => result.set(ip, { ip, label: '未知', isPrivate: false }));
    }
  }

  saveCache(cache);
  return result;
}

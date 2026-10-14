import * as echarts from 'echarts';

let ready = false;

/** 懒加载并注册 ECharts 世界地图底图（幂等，多页共用）。 */
export async function ensureWorldMap(): Promise<boolean> {
  if (ready || echarts.getMap('world')) {
    ready = true;
    return true;
  }
  try {
    const res = await fetch(`${import.meta.env.BASE_URL}maps/world.json`);
    if (!res.ok) throw new Error(`world.json HTTP ${res.status}`);
    echarts.registerMap('world', await res.json());
    ready = true;
    return true;
  } catch (e) {
    console.error('Failed to load world map geojson', e);
    return false;
  }
}

import { createRouter, createWebHashHistory, type RouteRecordRaw } from 'vue-router';

const routes: RouteRecordRaw[] = [
  { path: '/', redirect: '/overview' },
  { path: '/overview', name: 'overview', component: () => import('@/pages/Overview.vue'), meta: { title: '总览' } },
  { path: '/groups', name: 'groups', component: () => import('@/pages/Groups.vue'), meta: { title: '分组' } },
  { path: '/ranking', name: 'ranking', component: () => import('@/pages/Ranking.vue'), meta: { title: '排行榜' } },
  { path: '/fun', name: 'fun', component: () => import('@/pages/Fun.vue'), meta: { title: '趣味统计' } },
  { path: '/raw', name: 'raw', component: () => import('@/pages/Raw.vue'), meta: { title: '原始明细' } },
  { path: '/settings', name: 'settings', component: () => import('@/pages/Settings.vue'), meta: { title: '设置' } },
  { path: '/:pathMatch(.*)*', redirect: '/overview' },
];

export const router = createRouter({
  history: createWebHashHistory(),
  routes,
  scrollBehavior() {
    return { top: 0 };
  },
});

router.afterEach((to) => {
  const t = to.meta?.title as string | undefined;
  if (t) document.title = `${t} · 团队消耗看板`;
});

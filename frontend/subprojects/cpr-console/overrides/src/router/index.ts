import { createRouter, createWebHashHistory } from 'vue-router'
import { useAuthStore } from '@/stores/modules/auth'
export const router = createRouter({
  history: createWebHashHistory(),
  routes: [{
    path: '/', component: () => import('@/layout/index.vue'),
    children: [
      { path: '', redirect: '/accounts' },
      { path: 'accounts', name: 'accounts', component: () => import('@/views/accounts/index.vue') },
      { path: 'groups', name: 'groups', component: () => import('@/views/groups/index.vue') },
      { path: 'proxies', name: 'proxies', component: () => import('@/views/proxies/index.vue') },
      { path: 'usage', name: 'usage', component: () => import('@/views/usage/index.vue') },
      { path: 'settings', name: 'settings', component: () => import('@/views/settings/index.vue') },
      { path: 'settings/upstream', name: 'settings-upstream', component: () => import('@/views/settings/index.vue') },
    ],
  }, {
    path: '/login', name: 'login', component: () => import('@/sub2api/SessionRequired.vue'),
  }, { path: '/:pathMatch(.*)*', redirect: '/accounts' }],
})
router.beforeEach(async (to) => {
  if (to.name === 'login') return
  const auth = useAuthStore()
  if (!auth.sessionChecked && !await auth.checkAuth()) return { name: 'login' }
  if (!auth.isAdmin) return { name: 'login' }
})

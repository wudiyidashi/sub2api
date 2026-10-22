import { vLoading } from '@codex-proxy/ui'
import { createApp } from 'vue'
import App from './App.vue'
import { authPlugin } from './plugins/auth'
import { router } from './router'
import { pinia } from './stores'
import { useThemeStore } from './stores/modules/theme'
import { hostBridge } from './sub2api/bridge'
import { installHostEvents } from './sub2api/events'
import '@fontsource-variable/inter'
import '@fontsource-variable/jetbrains-mono'
import '@codex-proxy/ui/styles.css'
import './styles/index.css'

installHostEvents()
const app = createApp(App)
app.directive('loading', vLoading)
app.use(pinia)
const theme = useThemeStore(pinia)
theme.initializeTheme()
const syncTheme = () => theme.setThemeMode(hostBridge().theme())
syncTheme()
const observer = new MutationObserver(syncTheme)
observer.observe(window.parent.document.documentElement, { attributes: true, attributeFilter: ['class'] })
app.onUnmount(() => observer.disconnect())
app.use(router)
app.use(authPlugin)
app.mount('#app')

import { createApp } from 'vue'

import App from './app/App.vue'
import { router } from './app/router'
import './styles/tokens.css'

createApp(App).use(router).mount('#app')


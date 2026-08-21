import React from 'react'
import {createRoot} from 'react-dom/client'
import './style.css'
import App from './App'

const container = document.getElementById('root')
if (!container) throw new Error('Root element #root not found')

// Apply persisted theme before rendering so we don't flash.
const persisted = localStorage.getItem('app:theme')
const prefersDark = window.matchMedia?.('(prefers-color-scheme: dark)').matches
const initialTheme = persisted ?? (prefersDark ? 'dark' : 'light')
document.documentElement.classList.toggle('dark', initialTheme === 'dark')

createRoot(container).render(
    <React.StrictMode>
        <App/>
    </React.StrictMode>
)
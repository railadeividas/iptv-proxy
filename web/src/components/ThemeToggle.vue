<script setup>
import { theme } from '../theme'

// One button: flips between light and dark from what is showing now. Until the first click the theme is
// 'system' (follows the OS), so the first click goes to the opposite of what the OS shows.
function flip() {
  const dark = theme.value === 'dark' || (theme.value === 'system' && window.matchMedia('(prefers-color-scheme: dark)').matches)
  theme.value = dark ? 'light' : 'dark'
}
</script>

<template>
  <button class="ghost icon" title="Switch light / dark theme" aria-label="Switch light / dark theme" @click="flip">
    <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <!-- sun, top left -->
      <circle cx="8" cy="8" r="2.5" />
      <path d="M8 2v1.5M2 8h1.5M3.8 3.8l1 1M12.2 3.8l-1 1M3.8 12.2l1-1" />
      <!-- moon, bottom right -->
      <path d="M21 15.5A6 6 0 0 1 13.5 8a6 6 0 1 0 7.5 7.5z" transform="translate(0 3.5)" />
    </svg>
  </button>
</template>

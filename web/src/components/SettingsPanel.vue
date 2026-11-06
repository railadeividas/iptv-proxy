<script setup>
import IptvOrgStatus from './IptvOrgStatus.vue'
import SettingsForm from './SettingsForm.vue'

defineProps({ group: String })
</script>

<template>
  <div class="panel">
    <IptvOrgStatus v-if="group === 'iptv-org'" />
    <SettingsForm :key="group" :group="group" />
  </div>
</template>

<style scoped>
.panel { display:flex; flex-direction:column; gap:16px; }
</style>

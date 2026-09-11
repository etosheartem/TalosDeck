<script setup lang="ts">
import { ref, onMounted, onUnmounted } from "vue";
import { X } from "lucide-vue-next";
defineProps<{ title: string; wide?: boolean }>();
const emit = defineEmits<{ close: [] }>();
const element = ref<HTMLDialogElement>();
let previous: HTMLElement | null = null;
onMounted(() => {
  previous = document.activeElement as HTMLElement;
  element.value?.showModal();
});
onUnmounted(() => previous?.focus());
</script>
<template>
  <dialog
    ref="element"
    :class="['console-dialog', { wide }]"
    @cancel.prevent="emit('close')"
    @click="$event.target === element && emit('close')"
  >
    <header>
      <h2>{{ title }}</h2>
      <button class="icon-button" aria-label="Закрыть" @click="emit('close')">
        <X :size="18" />
      </button>
    </header>
    <div class="dialog-content"><slot /></div>
  </dialog>
</template>

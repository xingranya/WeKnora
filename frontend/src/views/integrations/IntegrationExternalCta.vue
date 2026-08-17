<template>
  <component
    :is="href ? 'a' : 'button'"
    :type="href ? undefined : 'button'"
    class="ext-cta"
    :class="`ext-cta--${variant}`"
    :href="href"
    :download="download"
    :rel="href ? 'noopener' : undefined"
    @click="emit('click')"
  >
    <span class="ext-cta__badge" aria-hidden="true">
      <slot name="icon" />
    </span>
    <span class="ext-cta__body">
      <span class="ext-cta__label">{{ label }}</span>
      <span v-if="hint" class="ext-cta__hint">{{ hint }}</span>
    </span>
    <span class="ext-cta__arrow-wrap" aria-hidden="true">
      <t-icon :name="trailingIcon" class="ext-cta__arrow" />
    </span>
  </component>
</template>

<script setup lang="ts">
withDefaults(
  defineProps<{
    label: string
    hint?: string
    variant?: 'chrome' | 'claw'
    href?: string
    download?: string
    trailingIcon?: string
  }>(),
  { variant: 'chrome', trailingIcon: 'jump' },
)

const emit = defineEmits<{
  click: []
}>()
</script>

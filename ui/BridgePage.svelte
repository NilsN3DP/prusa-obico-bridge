<script>
  import { onMount } from 'svelte';

  let { translate } = $props();
  let state = $state('loading');

  onMount(() => {
    const controller = new AbortController();
    let timer;
    async function refresh() {
      try {
        const response = await fetch('/api/v1/resources/sink/obico-bridge', {
          signal: controller.signal,
          cache: 'no-store',
        });
        if (!response.ok) throw new Error('Could not read bridge status');
        const resource = await response.json();
        state = !resource.enabled ? 'disabled' : resource.status?.serving ? 'ready' : 'waiting';
      } catch {
        if (!controller.signal.aborted) state = 'unavailable';
      }
      if (!controller.signal.aborted) timer = setTimeout(refresh, 10000);
    }
    void refresh();
    return () => {
      controller.abort();
      clearTimeout(timer);
    };
  });
</script>

<section class="mx-auto w-full max-w-3xl space-y-5 p-6">
  <h1 class="text-2xl font-semibold">Obico Bridge</h1>
  <p role="status" class="text-lg">{translate('bridge.' + state)}</p>
  <p>{translate('bridge.description')}</p>
  <a class="underline" href="/settings/extensions/com.github.aux.nilsn3dp.prusa-obico-bridge/settings">
    {translate('bridge.settings')}
  </a>
</section>

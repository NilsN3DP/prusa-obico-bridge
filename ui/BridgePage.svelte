<script>
  import { onMount } from 'svelte';
  import { Badge, Button, Card, CardContent, CardHeader, CardTitle, Progress } from '@prusa/connect-ui/primitives';

  let { translate: t, moduleHref } = $props();
  let overview = $state([]);
  let selected = $state('');
  let current = $state(null);
  let activeVideo = $state(null);
  let obico = $state(null);
  let saving = $state(false);
  let saveFailed = $state(false);
  let channels = $state(null);
  let notificationState = $state('');
  let notificationSave = $state(null);
  let providers = $state([]);
  let provider = $state('');
  let credential = $state('');
  const events = [
    'enabled', 'notify_on_failure_alert', 'notify_on_print_start', 'notify_on_print_done',
    'notify_on_print_cancelled', 'notify_on_print_pause', 'notify_on_print_resume',
    'notify_on_filament_change', 'notify_on_heater_status',
  ];
  let history = $state([]);
  let camera = $state('');
  let image = $state('');
  let state = $state('loading');
  let selectionRevision = 0;
  let printer = $derived(overview.find(item => item.id === selected));
  let alert = $derived(obico?.current?.alerted_at &&
    (!obico.current.alert_acknowledged_at || Date.parse(obico.current.alerted_at) > Date.parse(obico.current.alert_acknowledged_at)));
  let prints = $derived(obico?.configured ? (obico.recent ?? []).map(job => ({
    id: job.id,
    display_name: job.filename,
    outcome: job.cancelled_at ? 'cancelled' : job.finished_at ? 'finished' : 'unknown',
    alerted: Boolean(job.alerted_at),
    video: job.video_url,
    taggedVideo: job.tagged_video_url,
  })) : history);

  async function read(path, signal) {
    const response = await fetch(path, { signal, cache: 'no-store' });
    if (!response.ok) throw new Error('Read failed');
    return response.json();
  }

  async function loadNotifications(event) {
    if (!event.currentTarget.open || notificationState === 'loading') return;
    notificationState = 'loading';
    try {
      channels = await read(moduleHref + '/notifications');
      notificationState = 'ready';
      providers = await read(moduleHref + '/notification-providers').catch(() => []);
    } catch {
      notificationState = 'unavailable';
    }
  }

  async function addNotification(event) {
    event.preventDefault();
    notificationSave = 'new';
    notificationState = 'ready';
    try {
      const response = await fetch(moduleHref + '/notification-providers', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: provider, credential }),
      });
      if (!response.ok) throw new Error('Create failed');
      provider = '';
      credential = '';
      channels = await read(moduleHref + '/notifications');
      notificationState = 'created';
    } catch {
      notificationState = 'error';
    } finally {
      notificationSave = null;
    }
  }

  async function saveNotification(channel) {
    notificationSave = channel.id;
    notificationState = 'ready';
    try {
      const response = await fetch(moduleHref + '/notifications/' + channel.id, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(Object.fromEntries(events.map(key => [key, channel[key]]))),
      });
      if (!response.ok) throw new Error('Update failed');
      notificationState = 'saved';
    } catch {
      notificationState = 'error';
    } finally {
      notificationSave = null;
    }
  }

  async function testNotification(channel) {
    notificationSave = channel.id;
    notificationState = 'ready';
    try {
      const response = await fetch(moduleHref + '/notifications/' + channel.id + '/test', { method: 'POST' });
      if (!response.ok) throw new Error('Channel test failed');
      notificationState = 'testRequested';
    } catch {
      notificationState = 'testFailed';
    } finally {
      notificationSave = null;
    }
  }

  function select(id) {
    if (selected === id) return;
    selectionRevision++;
    selected = id;
    current = null;
    activeVideo = null;
    obico = null;
    saveFailed = false;
    history = [];
    camera = '';
    if (image) URL.revokeObjectURL(image);
    image = '';
  }

  async function sendMonitoringAction(action) {
    const id = selected;
    const revision = selectionRevision;
    const url = moduleHref + '/printers/' + encodeURIComponent(id);
    saving = true;
    saveFailed = false;
    try {
      const response = await fetch(url + '/monitoring', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(action),
      });
      if (!response.ok) throw new Error('Update failed');
      const updated = await read(url + '/obico');
      if (selectionRevision === revision) obico = updated;
    } catch {
      if (selectionRevision === revision) saveFailed = true;
    } finally {
      saving = false;
    }
  }

  onMount(() => {
    const controller = new AbortController();
    const { signal } = controller;
    let timer;
    async function refresh() {
      try {
        const bridge = await read(moduleHref + '/overview', signal);
        if (signal.aborted) return;
        overview = bridge.printers.map(item => ({ ...item, id: item.device_id }));
        if (!overview.some(item => item.id === selected)) {
          select(overview[0]?.id ?? '');
        }
        state = 'ready';
        if (!selected) return;

        const id = selected;
        const revision = selectionRevision;
        const path = '/api/v1/devices/' + encodeURIComponent(id) + '/jobs';
        const [jobs, recent, cameras, detection] = await Promise.allSettled([
          read(path, signal),
          read(path + '/history?limit=5', signal),
          read('/api/v1/cameras?device=' + encodeURIComponent(id), signal),
          read(moduleHref + '/printers/' + encodeURIComponent(id) + '/obico', signal),
        ]);
        if (signal.aborted || revision !== selectionRevision) return;
        current = jobs.status === 'fulfilled' ? jobs.value.current ?? null : null;
        obico = detection.status === 'fulfilled' ? detection.value : null;
        history = recent.status === 'fulfilled' ? recent.value.items : null;
        const allowed = cameras.status === 'fulfilled' ? cameras.value.items.filter(
          item => item.device_id === id && item.snapshot &&
            (!printer.camera || item.name === printer.camera),
        ) : [];
        camera = allowed.length === 1 ? allowed[0].name : '';
        if (camera) {
          const response = await fetch('/api/v1/cameras/' + encodeURIComponent(camera) + '/snapshot', {
            signal, cache: 'no-store',
          });
          const blob = response.status === 200 && response.headers.get('content-type')?.startsWith('image/jpeg')
            ? await response.blob() : null;
          if (signal.aborted || revision !== selectionRevision) return;
          if (image) URL.revokeObjectURL(image);
          image = blob ? URL.createObjectURL(blob) : '';
        } else if (image) {
          URL.revokeObjectURL(image);
          image = '';
        }
      } catch {
        if (!signal.aborted) state = 'unavailable';
      } finally {
        if (!signal.aborted) timer = setTimeout(refresh, 5000);
      }
    }
    void refresh();
    return () => {
      controller.abort();
      clearTimeout(timer);
      if (image) URL.revokeObjectURL(image);
    };
  });
</script>

<section class="mx-auto w-full max-w-5xl space-y-5 p-6">
  <header class="flex flex-wrap items-center justify-between gap-3">
    <h1 class="text-2xl font-semibold">Obico Bridge</h1>
    <a class="underline" href="/settings/extensions/com.github.aux.nilsn3dp.prusa-obico-bridge/settings">{t('bridge.settings')}</a>
  </header>

  {#if overview.length > 1}
    <label class="flex flex-wrap items-center gap-3">
      {t('bridge.printer')}
      <select class="rounded-md border bg-background p-2" value={selected} onchange={event => select(event.currentTarget.value)}>
        {#each overview as device (device.id)}
          <option value={device.id}>{device.name}</option>
        {/each}
      </select>
    </label>
  {/if}

  {#if state !== 'ready'}
    <p role="status">{t('bridge.' + state)}</p>
  {:else if !printer}
    <p role="status">{t('bridge.empty')}</p>
  {/if}

  {#if printer}
    <div class="grid gap-4 md:grid-cols-2">
      <Card>
        <CardHeader><CardTitle>{t('bridge.camera')}</CardTitle></CardHeader>
        <CardContent>
          {#if image}
            <img src={image} alt={t('bridge.camera')} class="aspect-video w-full rounded-md object-contain" />
          {:else}
            <p class="text-muted-foreground">{t('bridge.noImage')}</p>
          {/if}
        </CardContent>
      </Card>

      <Card>
        <CardHeader><CardTitle>{printer.name}</CardTitle></CardHeader>
        <CardContent class="space-y-4">
          <div class="flex flex-wrap gap-2">
            <Badge variant={printer.connected ? 'success' : 'secondary'}>{t(printer.connected ? 'bridge.online' : 'bridge.offline')}</Badge>
            <Badge variant={printer.obico_connected ? 'success' : 'warning'}>{t(printer.obico_connected ? 'bridge.obicoConnected' : 'bridge.obicoDisconnected')}</Badge>
            {#if obico?.configured}
              <Badge variant={obico.watching ? 'success' : 'secondary'}>{t(obico.watching ? 'bridge.watching' : 'bridge.notWatching')}</Badge>
            {/if}
          </div>
          {#if obico?.can_control}
            <Button variant="outline" size="sm" disabled={saving} onclick={() => sendMonitoringAction({ watching: !obico.watching })}>
              {t(obico.watching ? 'bridge.disableWatching' : 'bridge.enableWatching')}
            </Button>
          {/if}
          {#if saveFailed}<p role="alert">{t('bridge.saveFailed')}</p>{/if}
          {#if alert}
            <p role="status"><Badge variant="destructive">{t('bridge.alert')}</Badge></p>
            {#if obico.can_control}
              <Button variant="outline" size="sm" disabled={saving} onclick={() => sendMonitoringAction({ print_id: obico.current.id, acknowledge: true })}>
                {t('bridge.acknowledge')}
              </Button>
            {/if}
          {:else if obico === null}
            <p class="text-muted-foreground">{t('bridge.obicoUnavailable')}</p>
          {:else if !obico.configured}
            <p class="text-muted-foreground">{t('bridge.obicoUnconfigured')}</p>
          {/if}
          {#if obico?.can_control && obico.current}
            <Button variant="outline" size="sm" disabled={saving} onclick={() => sendMonitoringAction({ print_id: obico.current.id, muted: !obico.current.alert_muted_at })}>
              {t(obico.current.alert_muted_at ? 'bridge.unmute' : 'bridge.mute')}
            </Button>
          {/if}
          {#if current}
            <p class="break-all font-medium">{current.display_name || current.path}</p>
            <div class="flex items-center gap-3">
              <Progress value={current.progress} aria-label={t('bridge.progress')} />
              <span>{Math.round(current.progress)} %</span>
            </div>
          {:else}
            <p class="text-muted-foreground">{t('bridge.noJob')}</p>
          {/if}
        </CardContent>
      </Card>
    </div>

    <Card>
      <CardHeader><CardTitle>{t(obico?.configured ? 'bridge.obicoHistory' : 'bridge.history')}</CardTitle></CardHeader>
      <CardContent>
        {#if prints === null}
          <p>{t('bridge.unavailable')}</p>
        {:else if prints.length === 0}
          <p class="text-muted-foreground">{t('bridge.noHistory')}</p>
        {:else}
          <ul class="divide-y">
            {#each prints as job (job.id)}
              <li class="flex flex-wrap items-center justify-between gap-2 py-3">
                <span class="break-all">{job.display_name || job.path}</span>
                {#if job.alerted}<Badge variant="warning">{t('bridge.alert')}</Badge>{/if}
                <Badge variant={job.outcome === 'finished' ? 'success' : 'secondary'}>
                  {t('bridge.outcome.' + (['finished', 'failed', 'cancelled'].includes(job.outcome) ? job.outcome : 'unknown'))}
                </Badge>
                {#if job.video || job.taggedVideo}
                  <div class="flex flex-wrap gap-2">
                    {#if job.video}
                      <Button variant="outline" size="sm" onclick={() => activeVideo = { id: job.id, source: job.video }}>
                        {t('bridge.playTimelapse')}
                      </Button>
                    {/if}
                    {#if job.taggedVideo}
                      <Button variant="outline" size="sm" onclick={() => activeVideo = { id: job.id, source: job.taggedVideo }}>
                        {t('bridge.playTaggedTimelapse')}
                      </Button>
                    {/if}
                  </div>
                {/if}
                {#if activeVideo?.id === job.id}
                  <!-- svelte-ignore a11y_media_has_caption (Silent camera timelapses.) -->
                  <video class="w-full rounded-md" src={activeVideo.source} controls muted playsinline preload="metadata"></video>
                  <Button variant="ghost" size="sm" onclick={() => activeVideo = null}>{t('bridge.closeTimelapse')}</Button>
                {/if}
              </li>
            {/each}
          </ul>
        {/if}
      </CardContent>
    </Card>
  {/if}
</section>

<section class="mx-auto w-full max-w-5xl px-6 pb-6">
  <Card>
    <CardContent class="pt-4">
      <details ontoggle={loadNotifications}>
        <summary class="cursor-pointer font-medium">{t('notifications.title')}</summary>
        <div class="space-y-4 pt-4">
          {#if ['loading', 'unavailable', 'saved', 'created', 'error', 'testRequested', 'testFailed'].includes(notificationState)}
            <p role="status">{t('notifications.' + notificationState)}</p>
          {/if}
          {#if channels?.length === 0}
            <p>{t('notifications.empty')}</p>
          {/if}
          {#each channels ?? [] as channel (channel.id)}
            <fieldset class="space-y-2 rounded-md border p-4">
              <legend class="px-2 font-medium">{channel.name}</legend>
              {#each events as key (key)}
                <label class="flex items-center gap-2">
                  <input type="checkbox" bind:checked={channel[key]} />
                  {t('notifications.' + key)}
                </label>
              {/each}
              <Button variant="outline" size="sm" disabled={notificationSave !== null} onclick={() => saveNotification(channel)}>
                {t('notifications.save')}
              </Button>
              <Button variant="ghost" size="sm" disabled={notificationSave !== null} onclick={() => testNotification(channel)}>
                {t('notifications.sendTest')}
              </Button>
            </fieldset>
          {/each}
          {#if providers.length}
            <form class="space-y-3 border-t pt-4" onsubmit={addNotification}>
              <label class="flex flex-wrap items-center gap-3">
                {t('notifications.provider')}
                <select class="rounded-md border bg-background p-2" required bind:value={provider} onchange={() => credential = ''}>
                  <option value="">{t('notifications.chooseProvider')}</option>
                  {#each providers as option (option)}
                    <option value={option}>{t('notifications.provider.' + option)}</option>
                  {/each}
                </select>
              </label>
              {#if provider && provider !== 'email'}
                <label class="block">
                  {t('notifications.credential.' + provider)}
                  <input class="block w-full rounded-md border bg-background p-2" type="password" required maxlength="1024" autocomplete="off" bind:value={credential} />
                </label>
              {/if}
              <Button variant="outline" size="sm" type="submit" disabled={!provider || notificationSave !== null}>
                {t('notifications.add')}
              </Button>
            </form>
          {/if}
        </div>
      </details>
    </CardContent>
  </Card>
</section>

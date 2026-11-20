import BridgePage from './BridgePage.js';

export default function setup(host) {
  const moduleHref = '/api/v1/modules/com.github.aux.nilsn3dp.prusa-obico-bridge';
  host.register.page('obico-bridge', (anchor) => BridgePage(anchor, { translate: host.t, moduleHref }));
}

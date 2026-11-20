import BridgePage from './BridgePage.js';

export default function setup(host) {
  host.register.page('obico-bridge', (anchor) => BridgePage(anchor, { translate: host.t }));
}

// Neural Hive contract tests: token, capability registry, reputation registry.
const H = require('./helpers');

const HiveToken = artifacts.require('HiveToken');
const CapabilityRegistry = artifacts.require('CapabilityRegistry');
const ReputationRegistry = artifacts.require('ReputationRegistry');

const ZERO = '0x0000000000000000000000000000000000000000';
const CAP = ['1','2','3','4','5','6','7','8'];
const FAR = 9999999999;

async function advance(sec) {
  await new Promise((res, rej) => web3.currentProvider.send(
    { jsonrpc: '2.0', method: 'evm_increaseTime', params: [sec], id: Date.now() },
    (e, r) => (e ? rej(e) : res(r))));
  await new Promise((res, rej) => web3.currentProvider.send(
    { jsonrpc: '2.0', method: 'evm_mine', params: [], id: Date.now() },
    (e, r) => (e ? rej(e) : res(r))));
}

contract('HiveToken', (accounts) => {
  const admin = accounts[0];
  const other = accounts[1];

  it('mints the initial supply to the admin and exposes metadata', async () => {
    const t = await HiveToken.deployed();
    assert.equal(await t.name(), 'Neural Hive');
    assert.equal(await t.symbol(), 'HIVE');
    const bal = await t.balanceOf(admin);
    assert(bal.gt(web3.utils.toBN('0')), 'admin holds the initial supply');
  });

  it('restricts minting to MINTER_ROLE and enforces the supply cap', async () => {
    const t = await HiveToken.deployed();
    await t.mint(other, web3.utils.toWei('5', 'ether'), { from: admin });
    const bal = await t.balanceOf(other);
    assert.equal(bal.toString(), web3.utils.toWei('5', 'ether'));
    let threw = false;
    try { await t.mint(other, '1', { from: other }); } catch (e) { threw = true; }
    assert(threw, 'non-minter mint must revert');
    const cap = await t.MAX_SUPPLY();
    const supply = await t.totalSupply();
    const over = cap.sub(supply).add(web3.utils.toBN('1'));
    threw = false;
    try { await t.mint(admin, over.toString(), { from: admin }); } catch (e) { threw = true; }
    assert(threw, 'mint above MAX_SUPPLY must revert');
  });

  it('burns tokens from the caller', async () => {
    const t = await HiveToken.deployed();
    const before = await t.totalSupply();
    await t.burn(web3.utils.toWei('1', 'ether'), { from: other });
    const after = await t.totalSupply();
    assert.equal(before.sub(after).toString(), web3.utils.toWei('1', 'ether'));
  });
});

contract('CapabilityRegistry', (accounts) => {
  const admin = accounts[0];
  let reg;
  beforeEach(async () => { reg = await CapabilityRegistry.deployed(); });

  it('registers an agent when signer equals operator', async () => {
    const k = await H.newKey();
    await H.fund(admin, k.addr, '1');
    await reg.registerAgent(k.addr, CAP, web3.utils.toWei('0.1', 'ether'), 'http://agent', FAR, '0x', { from: k.addr });
    assert(await reg.isRegistered(k.addr));
    assert.equal(await reg.signerOf(k.addr), k.addr);
    assert(await reg.isActive(k.addr));
    const cap = await reg.capabilityOf(k.addr);
    assert.equal(cap[0].toString(), '1');
    assert.equal(cap[7].toString(), '8');
  });

  it('rejects duplicate registration and re-used signing keys', async () => {
    const k = await H.newKey();
    const k2 = await H.newKey();
    await H.fund(admin, k.addr, '1');
    await H.fund(admin, k2.addr, '1');
    await reg.registerAgent(k.addr, CAP, 1, 'http://a', FAR, '0x', { from: k.addr });
    let threw = false;
    try { await reg.registerAgent(k.addr, CAP, 1, 'http://a', FAR, '0x', { from: k.addr }); } catch (e) { threw = true; }
    assert(threw, 'duplicate registration must revert');
    threw = false;
    try { await reg.registerAgent(k.addr, CAP, 1, 'http://b', FAR, '0x', { from: k2.addr }); } catch (e) { threw = true; }
    assert(threw, 're-using a bound signing key must revert');
  });

  it('accepts a valid EIP-712 operator/signer binding and rejects a forged one', async () => {
    const op = await H.newKey();
    const signer = await H.newKey();
    const evil = await H.newKey();
    await H.fund(admin, op.addr, '1');
    const chainId = await web3.eth.getChainId();

    const goodSig = H.signRegistration(signer.priv, chainId, reg.address, op.addr, signer.addr, 0, FAR);
    await reg.registerAgent(signer.addr, CAP, 1, 'http://s', FAR, goodSig, { from: op.addr });
    assert.equal(await reg.signerOf(op.addr), signer.addr);

    const op2 = await H.newKey();
    const signer2 = await H.newKey();
    await H.fund(admin, op2.addr, '1');
    const forged = H.signRegistration(evil.priv, chainId, reg.address, op2.addr, signer2.addr, 0, FAR);
    let threw = false;
    try { await reg.registerAgent(signer2.addr, CAP, 1, 'http://s2', FAR, forged, { from: op2.addr }); } catch (e) { threw = true; }
    assert(threw, 'forged operator/signer binding must revert');
  });

  it('enforces MAX_PRICE and the signature deadline', async () => {
    const k = await H.newKey();
    await H.fund(admin, k.addr, '1');
    let threw = false;
    try { await reg.registerAgent(k.addr, CAP, web3.utils.toWei('2', 'ether'), 'http://x', FAR, '0x', { from: k.addr }); } catch (e) { threw = true; }
    assert(threw, 'price above MAX_PRICE must revert');
    const k2 = await H.newKey();
    await H.fund(admin, k2.addr, '1');
    threw = false;
    try { await reg.registerAgent(k2.addr, CAP, 1, 'http://x', 1, '0x', { from: k2.addr }); } catch (e) { threw = true; }
    assert(threw, 'expired registration deadline must revert');
  });

  it('lets an operator update capability, price, endpoint and active flag', async () => {
    const k = await H.newKey();
    const stranger = await H.newKey();
    await H.fund(admin, k.addr, '1');
    await H.fund(admin, stranger.addr, '1');
    await reg.registerAgent(k.addr, CAP, 1, 'http://before', FAR, '0x', { from: k.addr });
    const newCap = ['8','7','6','5','4','3','2','1'];
    await reg.updateCapability(newCap, { from: k.addr });
    const cap = await reg.capabilityOf(k.addr);
    assert.equal(cap[0].toString(), '8');
    await reg.updatePrice(web3.utils.toWei('0.5', 'ether'), { from: k.addr });
    assert.equal((await reg.priceOf(k.addr)).toString(), web3.utils.toWei('0.5', 'ether'));
    await reg.updateEndpoint('http://after', { from: k.addr });
    assert.equal(await reg.endpointOf(k.addr), 'http://after');
    await reg.setActive(false, { from: k.addr });
    assert.equal(await reg.isActive(k.addr), false);
    let threw = false;
    try { await reg.updatePrice(1, { from: stranger.addr }); } catch (e) { threw = true; }
    assert(threw, 'unregistered caller must not update an agent');
  });

  it('rotates the signing key only with a fresh binding signature', async () => {
    const k = await H.newKey();
    const newSigner = await H.newKey();
    const evil = await H.newKey();
    await H.fund(admin, k.addr, '1');
    await reg.registerAgent(k.addr, CAP, 1, 'http://r', FAR, '0x', { from: k.addr });
    const chainId = await web3.eth.getChainId();
    const nonce = await reg.registrationNonce(k.addr);
    const good = H.signRegistration(newSigner.priv, chainId, reg.address, k.addr, newSigner.addr, nonce, FAR);
    await reg.rotateSigner(newSigner.addr, FAR, good, { from: k.addr });
    assert.equal(await reg.signerOf(k.addr), newSigner.addr);

    const nonce2 = await reg.registrationNonce(k.addr);
    const bad = H.signRegistration(evil.priv, chainId, reg.address, k.addr, newSigner.addr, nonce2, FAR);
    let threw = false;
    try { await reg.rotateSigner(newSigner.addr, FAR, bad, { from: k.addr }); } catch (e) { threw = true; }
    assert(threw, 'forged rotation must revert');
  });

  it('records outcomes only for the OUTCOME_ROLE', async () => {
    const k = await H.newKey();
    const other = await H.newKey();
    await H.fund(admin, k.addr, '1');
    await reg.registerAgent(k.addr, CAP, 1, 'http://o', FAR, '0x', { from: k.addr });
    await reg.recordOutcome(k.addr, true, { from: admin });
    const s = await reg.statsOf(k.addr);
    assert.equal(s[0].toString(), '1');
    assert.equal(s[1].toString(), '1');
    let threw = false;
    try { await reg.recordOutcome(k.addr, true, { from: other.addr }); } catch (e) { threw = true; }
    assert(threw, 'unauthorised outcome recording must revert');
  });
});

contract('ReputationRegistry', (accounts) => {
  const admin = accounts[0];
  let rep;
  let a1, a2;
  beforeEach(async () => {
    rep = await ReputationRegistry.deployed();
    a1 = await H.newKey(); a2 = await H.newKey();
    await H.fund(admin, a1.addr, '1');
    await rep.registerAgent(a1.addr, { from: admin });
    await rep.registerAgent(a2.addr, { from: admin });
  });

  it('accepts trust signals only between known agents, once per epoch', async () => {
    await rep.submitTrustSignal(a2.addr, web3.utils.toWei('0.5', 'ether'), { from: a1.addr });
    let threw = false;
    try { await rep.submitTrustSignal(a2.addr, 1, { from: a1.addr }); } catch (e) { threw = true; }
    assert(threw, 'double signal in the same epoch must revert');
    const stranger = await H.newKey();
    threw = false;
    try { await rep.submitTrustSignal(a2.addr, 1, { from: stranger.addr }); } catch (e) { threw = true; }
    assert(threw, 'unknown agent cannot signal');
  });

  it('proposes and finalises an EigenTrust batch after the challenge window', async () => {
    await rep.setChallengeWindow(1, { from: admin });
    const scores = [web3.utils.toWei('0.7', 'ether'), web3.utils.toWei('0.3', 'ether')];
    const batchId = await rep.nextBatchId();
    await rep.proposeScores([a1.addr, a2.addr], scores, { from: admin });
    let threw = false;
    try { await rep.finalizeScores(batchId.toString(), { from: admin }); } catch (e) { threw = true; }
    assert(threw, 'finalise before the window elapses must revert');
    await advance(2);
    await rep.finalizeScores(batchId.toString(), { from: admin });
    assert.equal((await rep.effectiveScore(a1.addr)).toString(), scores[0]);
    assert.equal((await rep.effectiveScore(a2.addr)).toString(), scores[1]);
  });

  it('returns the neutral prior for agents never scored', async () => {
    const fresh = await H.newKey();
    const prior = await rep.PRIOR_SCORE();
    assert.equal((await rep.effectiveScore(fresh.addr)).toString(), prior.toString());
  });

  it('blocks a challenged batch until the arbiter resolves it', async () => {
    await rep.setChallengeWindow(1, { from: admin });
    const scores = [web3.utils.toWei('0.9', 'ether'), web3.utils.toWei('0.1', 'ether')];
    const batchId = await rep.nextBatchId();
    await rep.proposeScores([a1.addr, a2.addr], scores, { from: admin });
    await rep.challengeScores(batchId.toString(), { from: a1.addr });
    await advance(2);
    let threw = false;
    try { await rep.finalizeScores(batchId.toString(), { from: admin }); } catch (e) { threw = true; }
    assert(threw, 'challenged batch must not finalise on its own');
    await rep.resolveChallenge(batchId.toString(), false, { from: admin });
    const prior = await rep.PRIOR_SCORE();
    assert.equal((await rep.effectiveScore(a1.addr)).toString(), prior.toString(), 'rejected batch must not change scores');
  });

  it('enforces ORACLE_ROLE and ARBITER_ROLE', async () => {
    let threw = false;
    try { await rep.proposeScores([a1.addr], [1], { from: a1.addr }); } catch (e) { threw = true; }
    assert(threw, 'non-oracle proposal must revert');
    threw = false;
    try { await rep.resolveChallenge(0, true, { from: a1.addr }); } catch (e) { threw = true; }
    assert(threw, 'non-arbiter resolution must revert');
  });
});

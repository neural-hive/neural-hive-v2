// Neural Hive contract tests: TaskCoordinator lifecycle, Krum aggregation, VCG settlement, ICM.
const H = require('./helpers');

const HiveToken = artifacts.require('HiveToken');
const CapabilityRegistry = artifacts.require('CapabilityRegistry');
const StakingSettlement = artifacts.require('StakingSettlement');
const TaskCoordinator = artifacts.require('TaskCoordinator');
const MockTeleporterMessenger = artifacts.require('MockTeleporterMessenger');

const W = web3.utils.toWei;
const CAP = ['1','0','0','0','0','0','0','0'];
const FAR = 9999999999;

async function advance(sec) {
  await new Promise((res, rej) => web3.currentProvider.send(
    { jsonrpc: '2.0', method: 'evm_increaseTime', params: [sec], id: Date.now() },
    (e, r) => (e ? rej(e) : res(r))));
  await new Promise((res, rej) => web3.currentProvider.send(
    { jsonrpc: '2.0', method: 'evm_mine', params: [], id: Date.now() },
    (e, r) => (e ? rej(e) : res(r))));
}

contract('TaskCoordinator', (accounts) => {
  const admin = accounts[0];
  const stranger = accounts[3];
  let hive, reg, st, coord, teleporter;
  let chainId;
  let nonceSeq = 1000;

  beforeEach(async () => {
    hive = await HiveToken.deployed();
    reg = await CapabilityRegistry.deployed();
    st = await StakingSettlement.deployed();
    coord = await TaskCoordinator.deployed();
    teleporter = await MockTeleporterMessenger.deployed();
    chainId = await web3.eth.getChainId();
  });

  async function setupAgents(n, priceHive, stakeHive) {
    const keys = [];
    for (let i = 0; i < n; i++) {
      const k = await H.newKey();
      await H.fund(admin, k.addr, '1');
      await hive.transfer(k.addr, W((Number(stakeHive) + 1).toFixed(0), 'ether'), { from: admin });
      await reg.registerAgent(k.addr, CAP, W(priceHive, 'ether'), 'http://agent' + i, FAR, '0x', { from: k.addr });
      await hive.approve(st.address, W(stakeHive, 'ether'), { from: k.addr });
      await st.stake(W(stakeHive, 'ether'), { from: k.addr });
      keys.push(k);
    }
    return keys;
  }

  async function newRequest(budgetHive, tier) {
    await hive.approve(st.address, W(budgetHive, 'ether'), { from: admin });
    const id = await coord.nextRequestId();
    await coord.createRequest(web3.utils.keccak256('req' + id.toString()), W(budgetHive, 'ether'), tier, { from: admin });
    return id.toString();
  }

  async function addNumericStep(requestId, replication, f, maxPriceHive, numeric) {
    const id = await coord.nextStepId();
    const blk = await web3.eth.getBlock('latest');
    const deadline = Number(blk.timestamp) + 3600;
    await coord.addStep(requestId, CAP, replication, f, W(maxPriceHive, 'ether'), deadline, numeric, { from: admin });
    return { stepId: id.toString(), deadline };
  }

  async function respond(stepId, requestId, key, value, deadline, nonce) {
    const hash = H.outputHash(value);
    const sig = H.signResponse(key.priv, chainId, coord.address, {
      requestId, stepId, agent: key.addr, outputHash: hash, outputValue: value, nonce, deadline
    });
    await coord.submitResponse(stepId, key.addr, hash, value, nonce, deadline, sig, { from: admin });
    return hash;
  }

  it('runs a full request: escrow, assign, signed responses, Krum, VCG payout, refund', async () => {
    const keys = await setupAgents(5, '0.02', '5');
    const requestId = await newRequest('1', 1);
    const step = await addNumericStep(requestId, 5, 1, '0.06', true);
    for (let i = 0; i < 5; i++) {
      await coord.assignAgent(step.stepId, keys[i].addr, W('0.0' + (i + 2), 'ether'), { from: admin });
    }
    const V = '123456789';
    for (let i = 0; i < 5; i++) {
      await respond(step.stepId, requestId, keys[i], V, step.deadline, ++nonceSeq);
    }
    await coord.finalizeStep(step.stepId, { from: stranger });
    const info = await coord.stepInfo(step.stepId);
    assert.equal(info.status.toString(), '2', 'step finalized');
    assert.equal(info.consensusValue.toString(), V, 'Krum selects the honest value');
    assert.equal(info.winner, keys[0].addr, 'lowest bidder wins');
    assert.equal(info.runnerUp, keys[1].addr);
    assert.equal(info.payout.toString(), W('0.03', 'ether'), 'VCG pays the second-lowest bid');
    assert(await coord.isNonceUsed(keys[0].addr, nonceSeq - 4));

    const treasury = accounts[9];
    await st.setTreasury(treasury, { from: admin });
    const tb = await hive.balanceOf(treasury);
    await coord.settleRequest(requestId, { from: admin });
    assert(await coord.isPaid(step.stepId, keys[0].addr), 'winner marked paid');
    assert.equal((await hive.balanceOf(keys[0].addr)).toString(), W('1.03', 'ether'), 'winner balance = free stake + VCG payment');
    const ta = await hive.balanceOf(treasury);
    assert.equal(ta.sub(tb).toString(), W('0.0021', 'ether'), '7 percent protocol fee to treasury');
    const req = await coord.requestInfo(requestId);
    assert.equal(req.status.toString(), '1', 'request settled');
    assert.equal((await st.escrowAvailable(requestId)).toString(), '0', 'escrow fully drained or refunded');
    const stats = await reg.statsOf(keys[0].addr);
    assert.equal(stats[1].toString(), '1', 'success recorded on the capability registry');
  });

  it('Krum tolerates one lying agent in a five-agent committee', async () => {
    const keys = await setupAgents(5, '0.02', '5');
    const requestId = await newRequest('1', 1);
    const step = await addNumericStep(requestId, 5, 1, '0.05', true);
    for (let i = 0; i < 5; i++) {
      await coord.assignAgent(step.stepId, keys[i].addr, W('0.02', 'ether'), { from: admin });
    }
    const V = '555000';
    const LIE = '999000999';
    await respond(step.stepId, requestId, keys[0], LIE, step.deadline, ++nonceSeq);
    for (let i = 1; i < 5; i++) {
      await respond(step.stepId, requestId, keys[i], V, step.deadline, ++nonceSeq);
    }
    await coord.finalizeStep(step.stepId, { from: admin });
    const info = await coord.stepInfo(step.stepId);
    assert.equal(info.consensusValue.toString(), V, 'consensus is the honest value, not the outlier');
    assert.equal((await coord.consensusScoreCount(step.stepId)).toString(), '5', 'per-candidate Krum scores stored for audit');
    await coord.settleRequest(requestId, { from: admin });
    assert.equal(await coord.isPaid(step.stepId, keys[0].addr), false, 'the liar is not paid');
    assert(await coord.isPaid(step.stepId, keys[1].addr), 'an honest agent is paid');
  });

  it('rejects a response signed by a key that is not the registered signer', async () => {
    const keys = await setupAgents(1, '0.02', '5');
    const requestId = await newRequest('1', 1);
    const step = await addNumericStep(requestId, 1, 0, '0.05', true);
    await coord.assignAgent(step.stepId, keys[0].addr, W('0.02', 'ether'), { from: admin });
    const V = '42';
    const hash = H.outputHash(V);
    const evil = await H.newKey();
    const nonce = ++nonceSeq;
    const sig = H.signResponse(evil.priv, chainId, coord.address, {
      requestId, stepId: step.stepId, agent: keys[0].addr, outputHash: hash, outputValue: V, nonce, deadline: step.deadline
    });
    let threw = false;
    try { await coord.submitResponse(step.stepId, keys[0].addr, hash, V, nonce, step.deadline, sig, { from: admin }); } catch (e) { threw = true; }
    assert(threw, 'forged signature must be rejected');
    await respond(step.stepId, requestId, keys[0], V, step.deadline, ++nonceSeq);
    assert.equal((await coord.responseCount(step.stepId)).toString(), '1', 'the genuine response is accepted');
  });

  it('enforces one response per agent per step and per-agent nonce uniqueness', async () => {
    const keys = await setupAgents(1, '0.02', '5');
    const requestId = await newRequest('1', 1);
    const step = await addNumericStep(requestId, 1, 0, '0.05', true);
    await coord.assignAgent(step.stepId, keys[0].addr, W('0.02', 'ether'), { from: admin });
    const V = '7';
    const nonce = ++nonceSeq;
    const hash = await respond(step.stepId, requestId, keys[0], V, step.deadline, nonce);
    const sig = H.signResponse(keys[0].priv, chainId, coord.address, {
      requestId, stepId: step.stepId, agent: keys[0].addr, outputHash: hash, outputValue: V, nonce, deadline: step.deadline
    });
    let threw = false;
    try { await coord.submitResponse(step.stepId, keys[0].addr, hash, V, nonce, step.deadline, sig, { from: admin }); } catch (e) { threw = true; }
    assert(threw, 'replaying a consumed nonce must revert');
    const sig2 = H.signResponse(keys[0].priv, chainId, coord.address, {
      requestId, stepId: step.stepId, agent: keys[0].addr, outputHash: hash, outputValue: V, nonce: ++nonceSeq, deadline: step.deadline
    });
    threw = false;
    try { await coord.submitResponse(step.stepId, keys[0].addr, hash, V, nonceSeq, step.deadline, sig2, { from: admin }); } catch (e) { threw = true; }
    assert(threw, 'a second response from the same agent must revert');
  });

  it('restricts administration and enforces assignment eligibility', async () => {
    const keys = await setupAgents(1, '0.02', '5');
    const requestId = await newRequest('1', 1);
    let threw = false;
    try { await coord.addStep(requestId, CAP, 1, 0, 1, FAR, true, { from: stranger }); } catch (e) { threw = true; }
    assert(threw, 'non-coordinator cannot add a step');

    const tooMany = await coord.nextStepId();
    threw = false;
    try { await coord.addStep(requestId, CAP, 100, 0, 1, FAR, true, { from: admin }); } catch (e) { threw = true; }
    assert(threw, 'replication above MAX_COMMITTEE must revert');
    threw = false;
    try { await coord.addStep(requestId, CAP, 5, 5, 1, FAR, true, { from: admin }); } catch (e) { threw = true; }
    assert(threw, 'f above the BFT bound must revert');

    const step = await addNumericStep(requestId, 2, 0, '0.05', true);
    const unreg = await H.newKey();
    threw = false;
    try { await coord.assignAgent(step.stepId, unreg.addr, W('0.02', 'ether'), { from: admin }); } catch (e) { threw = true; }
    assert(threw, 'unregistered agent cannot be assigned');
    threw = false;
    try { await coord.assignAgent(step.stepId, keys[0].addr, W('0.02', 'ether'), { from: stranger }); } catch (e) { threw = true; }
    assert(threw, 'non-coordinator cannot assign');
    await coord.assignAgent(step.stepId, keys[0].addr, W('0.02', 'ether'), { from: admin });
    threw = false;
    try { await coord.assignAgent(step.stepId, keys[0].addr, W('0.02', 'ether'), { from: admin }); } catch (e) { threw = true; }
    assert(threw, 'duplicate assignment must revert');
    const keys2 = await setupAgents(1, '0.02', '5');
    threw = false;
    try { await coord.assignAgent(step.stepId, keys2[0].addr, W('0.5', 'ether'), { from: admin }); } catch (e) { threw = true; }
    assert(threw, 'a bid above the step price ceiling must revert');
  });

  it('aggregates non-numeric outputs by plurality of the output hash', async () => {
    const keys = await setupAgents(3, '0.02', '5');
    const requestId = await newRequest('1', 1);
    const step = await addNumericStep(requestId, 3, 0, '0.05', false);
    for (let i = 0; i < 3; i++) {
      await coord.assignAgent(step.stepId, keys[i].addr, W('0.02', 'ether'), { from: admin });
    }
    const answerHash = web3.utils.keccak256('the-summary');
    for (let i = 0; i < 3; i++) {
      const nonce = ++nonceSeq;
      const sig = H.signResponse(keys[i].priv, chainId, coord.address, {
        requestId, stepId: step.stepId, agent: keys[i].addr, outputHash: answerHash, outputValue: '0', nonce, deadline: step.deadline
      });
      await coord.submitResponse(step.stepId, keys[i].addr, answerHash, '0', nonce, step.deadline, sig, { from: admin });
    }
    await coord.finalizeStep(step.stepId, { from: admin });
    const info = await coord.stepInfo(step.stepId);
    assert.equal(info.status.toString(), '2');
    assert.equal(info.consensusHash, answerHash, 'the agreed output hash becomes the consensus');
  });

  it('fails a step when too few committee members answer before the deadline', async () => {
    const keys = await setupAgents(4, '0.02', '5');
    const requestId = await newRequest('1', 1);
    const step = await addNumericStep(requestId, 4, 0, '0.05', true);
    for (let i = 0; i < 4; i++) {
      await coord.assignAgent(step.stepId, keys[i].addr, W('0.02', 'ether'), { from: admin });
    }
    await respond(step.stepId, requestId, keys[0], '11', step.deadline, ++nonceSeq);
    await respond(step.stepId, requestId, keys[1], '11', step.deadline, ++nonceSeq);
    let threw = false;
    try { await coord.finalizeStep(step.stepId, { from: admin }); } catch (e) { threw = true; }
    assert(threw, 'cannot finalize while the committee is still answering');
    await advance(4000);
    await coord.finalizeStep(step.stepId, { from: admin });
    const info = await coord.stepInfo(step.stepId);
    assert.equal(info.status.toString(), '3', 'step marked failed when quorum was not reached');
  });

  it('only the Hive Snowball contract can invalidate a finalized step', async () => {
    const keys = await setupAgents(1, '0.02', '5');
    const requestId = await newRequest('1', 1);
    const step = await addNumericStep(requestId, 1, 0, '0.05', true);
    await coord.assignAgent(step.stepId, keys[0].addr, W('0.02', 'ether'), { from: admin });
    await respond(step.stepId, requestId, keys[0], '9', step.deadline, ++nonceSeq);
    await coord.finalizeStep(step.stepId, { from: admin });
    let threw = false;
    try { await coord.applyDisputeOutcome(step.stepId, true, { from: admin }); } catch (e) { threw = true; }
    assert(threw, 'only SNOWBALL_ROLE may invalidate a step');
  });

  it('accepts cross-chain requests only from the Teleporter and answers back over ICM', async () => {
    const source = web3.utils.keccak256('gamechain-L1');
    const origin = accounts[5];
    const budget = W('1', 'ether');
    const msg = web3.eth.abi.encodeParameters(
      ['bytes32', 'uint256', 'uint8', 'address'],
      [web3.utils.keccak256('icm-request'), budget, 1, admin]);
    let threw = false;
    try { await coord.receiveTeleporterMessage(source, origin, msg, { from: admin }); } catch (e) { threw = true; }
    assert(threw, 'only the configured TeleporterMessenger can deliver a message');
    await hive.approve(st.address, budget, { from: admin });
    const before = await coord.nextRequestId();
    await teleporter.deliver(source, origin, coord.address, msg, { from: admin });
    const after = await coord.nextRequestId();
    assert.equal(after.sub(before).toString(), '1', 'an interchain message creates exactly one request');
    const reqId = before.toString();
    const info = await coord.requestInfo(reqId);
    assert.equal(info.requester, origin, 'requester is the origin sender on the source L1');
    assert.equal((await st.escrowAvailable(reqId)).toString(), budget, 'budget escrowed on the C-Chain');
    const outBefore = await teleporter.outboundCount();
    await coord.sendInterchainAnswer(source, origin, reqId, { from: admin });
    const outAfter = await teleporter.outboundCount();
    assert.equal(outAfter.sub(outBefore).toString(), '1', 'the verified answer is forwarded back over ICM');
    threw = false;
    try { await coord.sendInterchainAnswer(source, origin, reqId, { from: stranger }); } catch (e) { threw = true; }
    assert(threw, 'only the coordinator role may answer');
  });
});

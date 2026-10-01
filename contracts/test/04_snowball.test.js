// Neural Hive contract tests: Hive Snowball dispute resolution, slashing and step invalidation.
const H = require('./helpers');

const HiveToken = artifacts.require('HiveToken');
const CapabilityRegistry = artifacts.require('CapabilityRegistry');
const StakingSettlement = artifacts.require('StakingSettlement');
const TaskCoordinator = artifacts.require('TaskCoordinator');
const HiveSnowball = artifacts.require('HiveSnowball');

const W = web3.utils.toWei;
const CAP = ['1','0','0','0','0','0','0','0'];
const FAR = 9999999999;
const TRUE_V = '4242';
const LIE_V = '999999';

contract('HiveSnowball', (accounts) => {
  const admin = accounts[0];
  let hive, reg, st, coord, snowball, chainId;
  let nonceSeq = 5000;

  beforeEach(async () => {
    hive = await HiveToken.deployed();
    reg = await CapabilityRegistry.deployed();
    st = await StakingSettlement.deployed();
    coord = await TaskCoordinator.deployed();
    snowball = await HiveSnowball.deployed();
    chainId = await web3.eth.getChainId();
  });

  async function newAgent(hiveAmt, stakeAmt) {
    const k = await H.newKey();
    await H.fund(admin, k.addr, '2');
    await hive.transfer(k.addr, W(hiveAmt, 'ether'), { from: admin });
    await reg.registerAgent(k.addr, CAP, W('0.02', 'ether'), 'http://dispute-agent', FAR, '0x', { from: k.addr });
    await hive.approve(st.address, W(stakeAmt, 'ether'), { from: k.addr });
    await st.stake(W(stakeAmt, 'ether'), { from: k.addr });
    return k;
  }

  async function lyingStep(liar, value) {
    await hive.approve(st.address, W('1', 'ether'), { from: admin });
    const requestId = (await coord.nextRequestId()).toString();
    await coord.createRequest(web3.utils.keccak256('dispute-req' + requestId), W('1', 'ether'), 1, { from: admin });
    const blk = await web3.eth.getBlock('latest');
    const deadline = Number(blk.timestamp) + 3600;
    const stepId = (await coord.nextStepId()).toString();
    await coord.addStep(requestId, CAP, 1, 0, W('0.05', 'ether'), deadline, true, { from: admin });
    await coord.assignAgent(stepId, liar.addr, W('0.02', 'ether'), { from: admin });
    const nonce = ++nonceSeq;
    const hash = H.outputHash(value);
    const sig = H.signResponse(liar.priv, chainId, coord.address, {
      requestId, stepId, agent: liar.addr, outputHash: hash, outputValue: value, nonce, deadline
    });
    await coord.submitResponse(stepId, liar.addr, hash, value, nonce, deadline, sig, { from: admin });
    await coord.finalizeStep(stepId, { from: admin });
    return { requestId, stepId };
  }

  it('overturns a wrong consensus, slashes the liar and invalidates the step', async () => {
    await snowball.setParams(3, 2, 50, W('1', 'ether'), 0, { from: admin });
    const liar = await newAgent('10', '5');
    const v1 = await newAgent('10', '1');
    const v2 = await newAgent('10', '1');
    const v3 = await newAgent('10', '1');
    const challenger = await newAgent('10', '1');

    const s = await lyingStep(liar, LIE_V);
    const stInfo = await coord.stepInfo(s.stepId);
    assert.equal(stInfo.consensusValue.toString(), LIE_V, 'single-agent step agreed on the lie');

    const honestPref = H.outputHash(TRUE_V);
    await hive.approve(snowball.address, W('1', 'ether'), { from: challenger.addr });
    const disputeId = (await snowball.nextDisputeId()).toString();
    await snowball.openDispute(s.stepId, honestPref, { from: challenger.addr });
    await snowball.castVote(disputeId, honestPref, { from: v1.addr });
    await snowball.castVote(disputeId, honestPref, { from: v2.addr });
    await snowball.castVote(disputeId, honestPref, { from: v3.addr });
    assert.equal((await snowball.voteCount(disputeId)).toString(), '4', 'challenger plus three verifiers voted');

    let resolved = false;
    for (let i = 0; i < 12 && !resolved; i++) {
      await snowball.snowballRound(disputeId, { from: admin });
      const d = await snowball.disputeInfo(disputeId);
      resolved = d[8];
    }
    const d = await snowball.disputeInfo(disputeId);
    assert(resolved && d[8], 'dispute resolved');
    assert.equal(d[7].toString(), '1', 'status upheld');
    assert.equal((await st.stakeOf(liar.addr)).toString(), '0', 'the lying agent was slashed to zero');
    const st2 = await coord.stepInfo(s.stepId);
    assert.equal(st2.status.toString(), '3', 'the invalidated step is marked failed');
    await coord.settleRequest(s.requestId, { from: admin });
    assert.equal(await coord.isPaid(s.stepId, liar.addr), false, 'the slashed agent is not paid');
  });

  it('rejects a baseless challenge and forfeits the challenger stake to the winner', async () => {
    await snowball.setParams(3, 2, 50, W('1', 'ether'), 0, { from: admin });
    const winner = await newAgent('10', '5');
    const v1 = await newAgent('10', '1');
    await newAgent('10', '1');
    const challenger = await newAgent('10', '2');

    const s = await lyingStep(winner, TRUE_V);
    const stInfo = await coord.stepInfo(s.stepId);
    const consensusHash = stInfo.consensusHash;

    const balBefore = await hive.balanceOf(winner.addr);
    const chBefore = await hive.balanceOf(challenger.addr);
    await hive.approve(snowball.address, W('1', 'ether'), { from: challenger.addr });
    const disputeId = (await snowball.nextDisputeId()).toString();
    await snowball.openDispute(s.stepId, H.outputHash(LIE_V), { from: challenger.addr });
    await snowball.castVote(disputeId, consensusHash, { from: v1.addr });

    let resolved = false;
    for (let i = 0; i < 12 && !resolved; i++) {
      await snowball.snowballRound(disputeId, { from: admin });
      const d = await snowball.disputeInfo(disputeId);
      resolved = d[8];
    }
    const d = await snowball.disputeInfo(disputeId);
    assert(d[8], 'dispute resolved');
    assert.equal(d[7].toString(), '2', 'status rejected');
    assert.equal((await hive.balanceOf(winner.addr)).sub(balBefore).toString(), W('1', 'ether'), 'winner receives the challenge stake');
    assert.equal(chBefore.sub(await hive.balanceOf(challenger.addr)).toString(), W('1', 'ether'), 'challenger forfeits the stake');
    assert.equal((await st.stakeOf(winner.addr)).toString(), W('5', 'ether'), 'no slashing on a rejected challenge');
  });

  it('rejects disputes from unregistered challengers and unregistered verifiers', async () => {
    const winner = await newAgent('10', '5');
    const s = await lyingStep(winner, TRUE_V);
    const outsider = await H.newKey();
    await H.fund(admin, outsider.addr, '2');
    let threw = false;
    try { await snowball.openDispute(s.stepId, H.outputHash(LIE_V), { from: outsider.addr }); } catch (e) { threw = true; }
    assert(threw, 'unregistered challenger rejected');

    const challenger = await newAgent('10', '2');
    await hive.approve(snowball.address, W('1', 'ether'), { from: challenger.addr });
    const disputeId = (await snowball.nextDisputeId()).toString();
    await snowball.openDispute(s.stepId, H.outputHash(LIE_V), { from: challenger.addr });
    threw = false;
    try { await snowball.castVote(disputeId, H.outputHash(LIE_V), { from: outsider.addr }); } catch (e) { threw = true; }
    assert(threw, 'unregistered verifier rejected');
    threw = false;
    try { await snowball.castVote(disputeId, H.outputHash(TRUE_V), { from: winner.addr }); } catch (e) { threw = true; }
    assert(threw, 'the challenged winner may not vote');
  });
});

// Neural Hive contract tests: staking, escrow, VCG-style payment settlement and slashing.
const H = require('./helpers');

const HiveToken = artifacts.require('HiveToken');
const StakingSettlement = artifacts.require('StakingSettlement');

async function advance(sec) {
  await new Promise((res, rej) => web3.currentProvider.send(
    { jsonrpc: '2.0', method: 'evm_increaseTime', params: [sec], id: Date.now() },
    (e, r) => (e ? rej(e) : res(r))));
  await new Promise((res, rej) => web3.currentProvider.send(
    { jsonrpc: '2.0', method: 'evm_mine', params: [], id: Date.now() },
    (e, r) => (e ? rej(e) : res(r))));
}

contract('StakingSettlement', (accounts) => {
  const admin = accounts[0];
  const stranger = accounts[2];
  const W = web3.utils.toWei;
  let hive, st;

  beforeEach(async () => {
    hive = await HiveToken.deployed();
    st = await StakingSettlement.deployed();
  });

  async function freshAgent(hiveAmt) {
    const k = await H.newKey();
    await H.fund(admin, k.addr, '1');
    await hive.transfer(k.addr, W(hiveAmt, 'ether'), { from: admin });
    return k;
  }

  it('accepts stake, rejects zero, and tracks the total', async () => {
    const k = await freshAgent('10');
    let threw = false;
    try { await st.stake(0, { from: k.addr }); } catch (e) { threw = true; }
    assert(threw, 'zero stake must revert');
    await hive.approve(st.address, W('10', 'ether'), { from: k.addr });
    await st.stake(W('10', 'ether'), { from: k.addr });
    assert.equal((await st.stakeOf(k.addr)).toString(), W('10', 'ether'));
    assert.equal((await st.availableStake(k.addr)).toString(), W('10', 'ether'));
  });

  it('locks and unlocks stake, and never lets locked stake be withdrawn', async () => {
    const k = await freshAgent('10');
    await hive.approve(st.address, W('10', 'ether'), { from: k.addr });
    await st.stake(W('10', 'ether'), { from: k.addr });
    await st.lockStake(k.addr, W('4', 'ether'), { from: admin });
    assert.equal((await st.lockedOf(k.addr)).toString(), W('4', 'ether'));
    assert.equal((await st.availableStake(k.addr)).toString(), W('6', 'ether'));
    let threw = false;
    try { await st.lockStake(k.addr, W('7', 'ether'), { from: admin }); } catch (e) { threw = true; }
    assert(threw, 'locking more than the free stake must revert');
    threw = false;
    try { await st.lockStake(k.addr, 1, { from: stranger }); } catch (e) { threw = true; }
    assert(threw, 'only SETTLEMENT_ROLE can lock stake');
    await st.unlockStake(k.addr, W('1', 'ether'), { from: admin });
    assert.equal((await st.lockedOf(k.addr)).toString(), W('3', 'ether'));
    await st.unlockStake(k.addr, W('100', 'ether'), { from: admin });
    assert.equal((await st.lockedOf(k.addr)).toString(), '0', 'unlock is clamped to the locked amount');
  });

  it('enforces the unbonding cooldown', async () => {
    const k = await freshAgent('10');
    await hive.approve(st.address, W('10', 'ether'), { from: k.addr });
    await st.stake(W('10', 'ether'), { from: k.addr });
    await st.requestUnstake(W('8', 'ether'), { from: k.addr });
    let threw = false;
    try { await st.withdraw({ from: k.addr }); } catch (e) { threw = true; }
    assert(threw, 'withdraw before the cooldown must revert');
    await advance(86500);
    await st.withdraw({ from: k.addr });
    assert.equal((await st.stakeOf(k.addr)).toString(), W('2', 'ether'));
  });

  it('rejects an unbond that would leave a dust stake below the minimum', async () => {
    const k = await freshAgent('10');
    await hive.approve(st.address, W('10', 'ether'), { from: k.addr });
    await st.stake(W('10', 'ether'), { from: k.addr });
    let threw = false;
    try { await st.requestUnstake(W('9.5', 'ether'), { from: k.addr }); } catch (e) { threw = true; }
    assert(threw, 'leaving less than minStake must revert');
  });

  it('escrows a requester budget and settles a task with a protocol fee', async () => {
    const payee = await H.newKey();
    const treasury = await st.treasury();
    const requestId = 777;
    await hive.approve(st.address, W('5', 'ether'), { from: admin });
    await st.depositEscrow(requestId, admin, W('5', 'ether'), { from: admin });
    const treasuryBefore = await hive.balanceOf(treasury);
    assert.equal((await st.escrowAvailable(requestId)).toString(), W('5', 'ether'));
    let threw = false;
    try { await st.depositEscrow(778, admin, 1, { from: stranger }); } catch (e) { threw = true; }
    assert(threw, 'only SETTLEMENT_ROLE can escrow');
    await st.payTask(requestId, payee.addr, W('2', 'ether'), W('0.14', 'ether'), { from: admin });
    assert.equal((await st.escrowAvailable(requestId)).toString(), W('2.86', 'ether'));
    assert.equal((await hive.balanceOf(payee.addr)).toString(), W('2', 'ether'));
    const treasuryAfter = await hive.balanceOf(treasury);
    assert.equal(treasuryAfter.sub(treasuryBefore).toString(), W('0.14', 'ether'));
    await st.refundEscrow(requestId, admin, W('2.86', 'ether'), { from: admin });
    assert.equal((await st.escrowAvailable(requestId)).toString(), '0');
    threw = false;
    try { await st.payTask(requestId, payee.addr, 1, 0, { from: admin }); } catch (e) { threw = true; }
    assert(threw, 'payment beyond the remaining escrow must revert');
  });

  it('slashes stake to a beneficiary and stops at the available amount', async () => {
    const k = await freshAgent('3');
    const victim = k.addr;
    await hive.approve(st.address, W('3', 'ether'), { from: k.addr });
    await st.stake(W('3', 'ether'), { from: k.addr });
    const before = await hive.balanceOf(admin);
    await st.slash(victim, W('100', 'ether'), admin, web3.utils.keccak256('reason'), { from: admin });
    assert.equal((await st.stakeOf(victim)).toString(), '0');
    const gained = (await hive.balanceOf(admin)).sub(before);
    assert.equal(gained.toString(), W('3', 'ether'), 'slash is capped at the available stake');
    assert.equal((await st.slashedTotal(victim)).toString(), W('3', 'ether'));
    let threw = false;
    try { await st.slash(victim, 1, admin, web3.utils.keccak256('x'), { from: stranger }); } catch (e) { threw = true; }
    assert(threw, 'only SLASHER_ROLE can slash');
  });

  it('lets the admin tune treasury, min stake and cooldown, and rejects others', async () => {
    await st.setTreasury(stranger, { from: admin });
    assert.equal(await st.treasury(), stranger);
    await st.setTreasury(admin, { from: admin });
    await st.setMinStake(W('0.5', 'ether'), { from: admin });
    assert.equal((await st.minStake()).toString(), W('0.5', 'ether'));
    await st.setUnstakeCooldown(60, { from: admin });
    assert.equal((await st.unstakeCooldown()).toString(), '60');
    let threw = false;
    try { await st.setMinStake(1, { from: stranger }); } catch (e) { threw = true; }
    assert(threw, 'non-admin cannot change params');
    await st.setMinStake(W('1', 'ether'), { from: admin });
    await st.setUnstakeCooldown(86400, { from: admin });
  });
});

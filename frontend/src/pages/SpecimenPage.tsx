import { useEffect, useMemo, useState } from 'react';
import { ENTITY_CONFIGS } from '../types/status';
import type { DomainRecord } from '../types/domain';
import { useSpecimenDetailStore } from '../stores/specimen-detail';
import { useAuth } from '../hooks/useAuth';
import { StatusBadge } from '../components/common/StatusBadge';
import { RiskTag } from '../components/common/RiskTag';
import { EmptyState } from '../components/common/EmptyState';
import { MetricCard } from '../components/common/MetricCard';
import { ConfirmDialog } from '../components/common/ConfirmDialog';
import { UiButton } from '../components/common/UiButton';
import { SplitDialog } from '../components/specimen/SplitDialog';
import { formatDate, nextStatus } from '../utils/format';

const SPLITTABLE_STATUSES = new Set(['received', 'testing']);

function formatQuantity(value: number | undefined, unit?: string): string {
  const amount = Number.isFinite(value) ? Number(value) : 0;
  const text = Number.isInteger(amount) ? String(amount) : amount.toFixed(4).replace(/0+$/, '').replace(/\.$/, '');
  return unit ? `${text} ${unit}` : text;
}

export default function SpecimenPage() {
  const config = ENTITY_CONFIGS[1];
  const { items, meta, loading, error, load, createRecord, transition, split } = useSpecimenDetailStore();
  const { session, hasRole } = useAuth();
  const [search, setSearch] = useState('');
  const [showCreate, setShowCreate] = useState(false);
  const [pendingTransition, setPendingTransition] = useState<{ item: DomainRecord; status: string } | null>(null);
  const [pendingSplit, setPendingSplit] = useState<DomainRecord | null>(null);

  useEffect(() => { void load(); }, [load]);

  const canOperate = hasRole('operator');
  const highRisk = useMemo(() => items.filter((item) => ['high', 'critical'].includes(item.riskLevel)).length, [items]);
  const totalAvailable = useMemo(() => items.reduce((sum, item) => sum + (item.availableQuantity ?? 0), 0), [items]);

  const createDemo = async () => {
    const now = Date.now();
    await createRecord({
      code: `SP-${now.toString().slice(-6)}`,
      name: '新增检验样本',
      description: '通过前端工作台创建的检验样本记录',
      facility: '默认检验区', owner: session?.displayName || '现场操作员', category: '常规', riskLevel: 'medium',
      metricValue: 25, metricUnit: 'unit', effectiveAt: new Date().toISOString(), evidence: '已完成接收前证据核对', relatedCode: '',
      quantity: 100,
    });
    setSearch('');
    setShowCreate(false);
  };

  const canSplit = (item: DomainRecord) => canOperate
    && SPLITTABLE_STATUSES.has(item.status)
    && (item.availableQuantity ?? 0) > 0;

  const splitBlockReason = (item: DomainRecord) => {
    if (!canOperate) return '只读';
    if (!SPLITTABLE_STATUSES.has(item.status)) return '仅接收/检测中可分装';
    if ((item.availableQuantity ?? 0) <= 0) return '可用量不足';
    return '';
  };

  const confirmTransition = async () => {
    if (!pendingTransition) return;
    await transition(pendingTransition.item, pendingTransition.status);
    setPendingTransition(null);
  };

  return <main className="workspace">
    <header className="page-header">
      <div>
        <p className="eyebrow">业务工作台</p>
        <h1>{config.label}</h1>
        <p>管理样本接收、状态迁移与分装：母样余量扣减、连续编号子样生成和来源记录一次完成。</p>
      </div>
      {canOperate
        ? <UiButton onClick={() => setShowCreate(true)}>新增{config.label}</UiButton>
        : <span className="access-note">只读权限</span>}
    </header>

    <section className="metrics">
      <MetricCard label="样本总数" value={meta.total} detail="当前筛选范围" />
      <MetricCard label="高风险" value={highRisk} detail="需要优先复核" />
      <MetricCard label="剩余可用量合计" value={formatQuantity(Math.round(totalAvailable * 10000) / 10000)} detail="全部未处置样本余量" />
    </section>

    <section className="toolbar">
      <input aria-label="搜索" placeholder="搜索检验样本编码或名称" value={search} onChange={(event) => setSearch(event.target.value)} />
      <UiButton onClick={() => void load(search)}>查询</UiButton>
      <button className="link-button" onClick={() => { setSearch(''); void load(); }}>重置</button>
    </section>
    {error && <div className="alert" role="alert">{error}</div>}

    <section className="table-shell" aria-busy={loading}>
      <table>
        <thead><tr>
          <th>编码 / 名称</th><th>状态</th><th>风险</th><th>可用量</th><th>来源</th><th>子样清单</th><th>阻断原因</th><th>责任人</th><th>更新时间</th><th>操作</th>
        </tr></thead>
        <tbody>
          {items.map((item) => {
            const next = nextStatus(item.status, config.primaryTransitions);
            const sequence = item.splitSequence ?? 0;
            const sequenceLabel = sequence > 0 ? String(sequence).padStart(2, '0') : '';
            return <tr key={item.id}>
              <td><strong>{item.code}</strong>{sequence > 0 && <small>子样 #{sequenceLabel}</small>}<small>{item.name}</small><small>{item.facility}</small></td>
              <td><StatusBadge status={item.status} /></td>
              <td><RiskTag level={item.riskLevel} /></td>
              <td>
                <strong>{formatQuantity(item.availableQuantity, item.quantityUnit)}</strong>
                <small>入库 {formatQuantity(item.quantity, item.quantityUnit)}</small>
              </td>
              <td>{item.parentId
                ? <span className="lineage">母样 <strong>{item.parentCode || `#${item.parentId}`}</strong></span>
                : <span className="muted">原始接收</span>}</td>
              <td>{item.children && item.children.length > 0
                ? <ul className="child-list">
                    {item.children.map((child) => <li key={child.id} className={child.disposed ? 'child-list__item child-list__item--done' : 'child-list__item'}>
                      <code>{child.code}</code>
                      <small>{child.status} · {formatQuantity(child.availableQuantity, child.quantityUnit)}</small>
                    </li>)}
                  </ul>
                : <span className="muted">-</span>}</td>
              <td>{item.blockReasons && item.blockReasons.length > 0
                ? <ul className="block-list">{item.blockReasons.map((reason, index) => <li key={index}>{reason}</li>)}</ul>
                : <span className="muted">无</span>}</td>
              <td>{item.owner}</td>
              <td>{formatDate(item.updatedAt)}</td>
              <td>
                <div className="row-actions">
                  {next && canOperate
                    ? <button className="table-action" onClick={() => setPendingTransition({ item, status: next })}>推进至 {next}</button>
                    : <span className="muted">{next ? '只读' : '流程结束'}</span>}
                  {canSplit(item)
                    ? <button className="table-action" onClick={() => setPendingSplit(item)}>分装</button>
                    : <span className="muted">{splitBlockReason(item)}</span>}
                </div>
              </td>
            </tr>;
          })}
          {!items.length && !loading && <EmptyState message="暂无记录" colSpan={10} />}
        </tbody>
      </table>
      {loading && <div className="loading">正在同步业务数据…</div>}
    </section>

    <ConfirmDialog open={showCreate} title={`新增${config.label}`} onCancel={() => setShowCreate(false)} onConfirm={() => void createDemo().catch(() => undefined)}>
      <p>将创建一条初始可用量为 100 的 received 检验样本，包含完整责任人、风险和证据信息。</p>
    </ConfirmDialog>

    <ConfirmDialog open={Boolean(pendingTransition)} title="确认状态迁移" onCancel={() => setPendingTransition(null)} onConfirm={() => void confirmTransition().catch(() => undefined)}>
      <p>状态迁移会写入不可覆盖的版本与审计日志。</p>
      <strong>{pendingTransition?.item.status} → {pendingTransition?.status}</strong>
    </ConfirmDialog>

    {pendingSplit && <SplitDialog item={pendingSplit} busy={loading}
      onCancel={() => setPendingSplit(null)}
      onConfirm={async (parts, reason, clientToken) => {
        await split(pendingSplit, parts, reason, clientToken);
        setPendingSplit(null);
      }} />}
  </main>;
}

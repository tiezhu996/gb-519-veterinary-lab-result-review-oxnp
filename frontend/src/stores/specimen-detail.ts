
import { create } from 'zustand';
import { listSpecimen, splitSpecimen } from '../api/specimen';
import { request } from '../api/client';
import type { ApiEnvelope, DomainRecord, PageMeta } from '../types/domain';

interface SpecimenState {
  items: DomainRecord[];
  meta: PageMeta;
  loading: boolean;
  error: string;
  load: (search?: string) => Promise<void>;
  createRecord: (input: Partial<DomainRecord>) => Promise<void>;
  transition: (item: DomainRecord, status: string) => Promise<void>;
  split: (item: DomainRecord, parts: number[], reason: string, clientToken: string) => Promise<void>;
}

export const useSpecimenDetailStore = create<SpecimenState>((set, get) => ({
  items: [], meta: { page: 1, pageSize: 20, total: 0 }, loading: false, error: '',
  load: async (search = '') => {
    set({ loading: true, error: '' });
    try {
      const result = await listSpecimen(1, 50, search);
      set({ items: result.data, meta: result.meta || { page: 1, pageSize: result.data.length, total: result.data.length }, loading: false });
    } catch (error) {
      set({ error: error instanceof Error ? error.message : String(error), loading: false });
    }
  },
  createRecord: async (input) => {
    set({ loading: true, error: '' });
    try {
      await request<DomainRecord>('/specimens', { method: 'POST', body: JSON.stringify(input) });
      await get().load();
    } catch (error) {
      set({ error: error instanceof Error ? error.message : String(error), loading: false });
      throw error;
    }
  },
  transition: async (item, status) => {
    set({ loading: true, error: '' });
    try {
      await request<DomainRecord>(`/specimens/${item.id}/transition`, {
        method: 'POST', body: JSON.stringify({ status, expectedVersion: item.version, reason: '前端工作台人工确认' }),
      });
      await get().load();
    } catch (error) {
      set({ error: error instanceof Error ? error.message : String(error), loading: false });
      throw error;
    }
  },
  split: async (item, parts, reason, clientToken) => {
    set({ loading: true, error: '' });
    try {
      await splitSpecimen(item.id, { expectedVersion: item.version, parts, reason, clientToken });
      await get().load();
    } catch (error) {
      set({ error: error instanceof Error ? error.message : String(error), loading: false });
      throw error;
    }
  },
}));

export type { ApiEnvelope };

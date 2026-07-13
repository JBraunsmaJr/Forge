<script lang="ts">
    import { connStatus, currentView, sidebarOpen, currentUser, authRequired } from '../stores';
    import { Menu, X, LogOut, User as UserIcon } from '@lucide/svelte';
    import { api } from '../api';

    const statusLabels = {
        idle: 'idle',
        connecting: 'connecting…',
        live: 'live',
        reconnecting: 'reconnecting…',
        done: 'complete',
        error: 'error'
    };

    async function handleLogout() {
        await api.logout();
        currentUser.set(null);
        authRequired.set(true);
    }
</script>

<header>
    {#if $currentView === 'runs'}
        <button class="menu-btn" on:click={() => sidebarOpen.update(v => !v)}>
            {#if $sidebarOpen}
                <X size={20} />
            {:else}
                <Menu size={20} />
            {/if}
        </button>
    {/if}
    <h1>Forge</h1>
    <span id="conn-status">
        <span id="conn-dot" class:off={$connStatus === 'idle' || $connStatus === 'connecting' || $connStatus === 'reconnecting' || $connStatus === 'error'}></span>
        <span id="conn-label">{statusLabels[$connStatus]}</span>
    </span>

    {#if $currentUser}
        <div class="user-nav">
            <div class="user-profile">
                <div class="avatar">
                    <UserIcon size={14} />
                </div>
                <span class="name">{$currentUser.name}</span>
            </div>
            <button class="logout-btn" on:click={handleLogout} title="Logout">
                <LogOut size={16} />
            </button>
        </div>
    {/if}
</header>

<style>
    header {
        background: var(--surface);
        border-bottom: 1px solid var(--border);
        padding: 0 20px;
        height: 52px;
        display: flex;
        align-items: center;
        gap: 16px;
        flex-shrink: 0;
    }
    header h1 {
        font-size: 16px;
        font-weight: 700;
        letter-spacing: .5px;
        color: var(--accent);
    }
    .menu-btn {
        display: none;
        background: none;
        border: none;
        color: var(--muted);
        cursor: pointer;
        padding: 8px;
        margin-left: -12px;
        border-radius: 4px;
    }
    .menu-btn:hover {
        background: var(--surface2);
        color: var(--text);
    }
    @media (max-width: 768px) {
        .menu-btn {
            display: flex;
            align-items: center;
            justify-content: center;
        }
        header {
            padding: 0 16px;
        }
    }
    #conn-status {
        margin-left: auto;
        font-size: 12px;
        color: var(--muted);
        display: flex;
        align-items: center;
        gap: 6px;
        padding-right: 16px;
        border-right: 1px solid var(--border);
    }
    .user-nav {
        display: flex;
        align-items: center;
        gap: 12px;
        margin-left: 16px;
    }
    .user-profile {
        display: flex;
        align-items: center;
        gap: 8px;
    }
    .avatar {
        width: 24px;
        height: 24px;
        border-radius: 50%;
        background: var(--surface2);
        display: flex;
        align-items: center;
        justify-content: center;
        color: var(--muted);
    }
    .name {
        font-size: 13px;
        color: var(--text);
        font-weight: 500;
    }
    .logout-btn {
        background: none;
        border: none;
        color: var(--muted);
        cursor: pointer;
        padding: 4px;
        display: flex;
        align-items: center;
        justify-content: center;
        border-radius: 4px;
    }
    .logout-btn:hover {
        background: var(--surface2);
        color: var(--red);
    }
    #conn-dot {
        width: 7px;
        height: 7px;
        border-radius: 50%;
        background: var(--green);
    }
    #conn-dot.off {
        background: var(--red);
    }
</style>

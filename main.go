package main

import (
    "context"
    "database/sql"
    "encoding/json"
    "fmt"
    "log"
    "math/big"
    "net/http"
    "os"
    "strconv"
    "time"
    _ "github.com/lib/pq"
    "github.com/ethereum/go-ethereum/core/types"
    "github.com/ethereum/go-ethereum/ethclient"
)

// --- Configuration ---
var (
    db          *sql.DB
    dbUrl       = os.Getenv("DATABASE_URL")
    adminUser   = os.Getenv("ADMIN_USER")
    adminPass   = os.Getenv("ADMIN_PASS")
    ownerWallet = os.Getenv("OWNER_WALLET") // Fee destination
    ethClient   *ethclient.Client
)

func main() {
    var err error
    
    // 1. Connect DB
    db, err = sql.Open("postgres", dbUrl)
    if err != nil {
        log.Fatal("DB Connection Error:", err)
    }
    defer db.Close()
    if err = db.Ping(); err != nil {
        log.Fatal("DB Ping Error:", err)
    }

    // 2. Connect Ethereum
    ethClient, err = ethclient.Dial("https://cloudflare-eth.com")
    if err != nil {
        log.Fatal("Ethereum Connection Error:", err)
    }
    log.Println("A2A INFRASTRUCTURE ONLINE")

    // 3. Schema & Seed Data (Providers)
    initSchema()
    seedProviders()

    // 4. Background Watcher
    go monitorBlockchain()

    // 5. Static Assets
    fs := http.FileServer(http.Dir("."))
    http.Handle("/logo.png", fs)
    http.Handle("/background.png", fs)

    // 6. Routes
    http.HandleFunc("/", indexHandler)
    
    // Public A2A APIs
    http.HandleFunc("/api/providers", getProviders) // Get list of GPUs
    http.HandleFunc("/api/rent", handleRent)       // Agent pays Provider
    http.HandleFunc("/api/balance", checkRealBalance)
    
    // Admin APIs
    http.HandleFunc("/api/admin/overview", adminAuth(handleAdminOverview))
    http.HandleFunc("/api/admin/a2a-tx", adminAuth(handleA2ATransactions)) // Specific A2A logs

    // 7. Start
    port := os.Getenv("PORT")
    if port == "" { port = "8080" }
    log.Printf("A2A MARKETPLACE ACTIVE ON :%s", port)
    log.Fatal(http.ListenAndServe(":"+port, nil))
}

// --- Schema & Logic ---

func initSchema() {
    query := `
    CREATE TABLE IF NOT EXISTS agents (
        id SERIAL PRIMARY KEY,
        wallet TEXT UNIQUE,
        balance BIGINT DEFAULT 0,
        banned BOOLEAN DEFAULT FALSE
    );
    CREATE TABLE IF NOT EXISTS providers (
        id SERIAL PRIMARY KEY,
        wallet TEXT UNIQUE, -- The provider getting paid
        node_id TEXT,
        gpu_model TEXT,
        status TEXT DEFAULT 'online',
        price_wei BIGINT
    );
    CREATE TABLE IF NOT EXISTS a2a_jobs (
        id SERIAL PRIMARY KEY,
        from_wallet TEXT, -- Renter
        to_wallet TEXT,   -- Provider
        fee_wei BIGINT,   -- Owner's cut
        amount_wei BIGINT, -- Total paid
        timestamp TIMESTAMP DEFAULT NOW()
    );
    CREATE TABLE IF NOT EXISTS activity_log (
        id SERIAL PRIMARY KEY,
        description TEXT,
        timestamp TIMESTAMP DEFAULT NOW()
    );
    `
    db.Exec(query)
}

// Seed fake GPU Providers for Marketplace
func seedProviders() {
    // Check if empty
    var count int
    db.QueryRow("SELECT COUNT(*) FROM providers").Scan(&count)
    if count == 0 {
        log.Println("Seeding A2A GPU Nodes...")
        db.Exec("INSERT INTO providers (node_id, wallet, gpu_model, status, price_wei) VALUES ($1, $2, $3, $4, $5)",
            "NODE_ALPHA", "0xProvider1...", "NVIDIA A100", "ONLINE", 10000000000000000)
        db.Exec("INSERT INTO providers (node_id, wallet, gpu_model, status, price_wei) VALUES ($1, $2, $3, $4, $5)",
            "NODE_BRAVO", "0xProvider2...", "RTX 3090", "ONLINE", 5000000000000000)
        db.Exec("INSERT INTO providers (node_id, wallet, gpu_model, status, price_wei) VALUES ($1, $2, $3, $4, $5)",
            "NODE_CHARLIE", "0xProvider3...", "H100", "ONLINE", 20000000000000000)
    }
}

func monitorBlockchain() {
    var lastBlock int64 = 0
    for {
        header, _ := ethClient.HeaderByNumber(context.Background(), nil)
        if header == nil { time.Sleep(10 * time.Second); continue }
        current := header.Number.Int64()
        if lastBlock == 0 { lastBlock = current - 10 }
        
        for b := lastBlock + 1; b <= current; b++ {
            block, _ := ethClient.BlockByNumber(context.Background(), big.NewInt(b))
            for _, tx := range block.Transactions() {
                if tx.To() != nil && tx.To().Hex() == ownerWallet && tx.Value().Sign() > 0 {
                    sender := tx.From().Hex()
                    amount := tx.Value()
                    db.Exec(`INSERT INTO agents (wallet, balance) VALUES ($1, $2) 
                        ON CONFLICT (wallet) DO UPDATE SET balance = agents.balance + $2`, 
                        sender, amount.String())
                    db.Exec("INSERT INTO activity_log (description) VALUES ($1)", fmt.Sprintf("DEPOSIT: %s credits loaded", sender))
                }
            }
            lastBlock = b
        }
        time.Sleep(10 * time.Second)
    }
}

// --- A2A API: Get Providers ---
func getProviders(w http.ResponseWriter, r *http.Request) {
    rows, _ := db.Query("SELECT id, node_id, wallet, gpu_model, status, price_wei FROM providers WHERE status = 'ONLINE'")
    defer rows.Close()
    
    var nodes []map[string]interface{}
    for rows.Next() {
        var id int
        var node, wallet, gpu, status string
        var price int64
        rows.Scan(&id, &node, &wallet, &gpu, &status, &price)
        nodes = append(nodes, map[string]interface{}{
            "id": node, "wallet": wallet, "gpu": gpu, "price": price, "status": status,
        })
    }
    json.NewEncoder(w).Encode(nodes)
}

// --- A2A API: Rent (Peer to Peer Payment) ---
func handleRent(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost { return }
    
    var req struct {
        RenterWallet  string `json:"renter_wallet"`
        ProviderID    string `json:"provider_id"`
        ProviderWallet string `json:"provider_wallet"`
    }
    json.NewDecoder(r.Body).Decode(&req)

    // 1. Check Renter Balance
    var renterBal int64
    db.QueryRow("SELECT balance FROM agents WHERE wallet = $1", req.RenterWallet).Scan(&renterBal)
    if renterBal == 0 { http.Error(w, "INSUFFICIENT CREDITS", 400); return }

    // 2. Get Provider Price
    var priceWei int64
    db.QueryRow("SELECT price_wei FROM providers WHERE node_id = $1", req.ProviderID).Scan(&priceWei)

    if renterBal < priceWei {
        http.Error(w, "INSUFFICIENT FUNDS FOR GPU", 400)
        return
    }

    // 3. Calculate Fee (1% for Owner)
    feeAmount := big.NewInt(priceWei)
    feeAmount.Div(feeAmount, big.NewInt(100)) // / 100

    // 4. Execute A2A Transfer
    // Renter pays Price.
    // Provider gets (Price - Fee).
    // Owner gets Fee.
    
    tx, _ := db.Begin()
    
    // Deduct from Renter
    tx.Exec("UPDATE agents SET balance = balance - $1 WHERE wallet = $2", priceWei, req.RenterWallet)
    
    // Pay Provider (Net)
    providerNet := priceWei - feeAmount.Int64()
    tx.Exec(`INSERT INTO agents (wallet, balance) VALUES ($1, $2) 
        ON CONFLICT (wallet) DO UPDATE SET balance = agents.balance + $2`, 
        req.ProviderWallet, providerNet)
    
    // Log Transaction
    tx.Exec("INSERT INTO a2a_jobs (from_wallet, to_wallet, fee_wei, amount_wei) VALUES ($1, $2, $3, $4)", 
        req.RenterWallet, req.ProviderWallet, feeAmount.Int64(), priceWei)
    
    // Log Activity
    tx.Exec("INSERT INTO activity_log (description) VALUES ($1)", 
        fmt.Sprintf("A2A TRANSFER: %s -> %s | FEE: %s Wei", req.RenterWallet, req.ProviderWallet, feeAmount.String()))
    
    tx.Commit()

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]string{"status": "rented", "gpu": req.ProviderID, "paid_to": req.ProviderWallet})
}

func checkRealBalance(w http.ResponseWriter, r *http.Request) {
    wallet := r.URL.Query().Get("addr")
    var dbBal int64
    db.QueryRow("SELECT balance FROM agents WHERE wallet = $1", wallet).Scan(&dbBal)
    json.NewEncoder(w).Encode(map[string]interface{}{"balance": dbBal})
}

// --- Admin Surveillance ---

func handleAdminOverview(w http.ResponseWriter, r *http.Request) {
    var agents, providers int
    var totalFeeCollected int64
    db.QueryRow("SELECT COUNT(*) FROM agents").Scan(&agents)
    db.QueryRow("SELECT COUNT(*) FROM providers WHERE status='ONLINE'").Scan(&providers)
    db.QueryRow("SELECT COALESCE(SUM(fee_wei), 0) FROM a2a_jobs").Scan(&totalFeeCollected)
    
    json.NewEncoder(w).Encode(map[string]interface{}{
        "agents": agents,
        "nodes": providers,
        "revenue": totalFeeCollected,
    })
}

func handleA2ATransactions(w http.ResponseWriter, r *http.Request) {
    rows, _ := db.Query("SELECT from_wallet, to_wallet, fee_wei, amount_wei, timestamp FROM a2a_jobs ORDER BY id DESC LIMIT 20")
    defer rows.Close()
    
    var txs []map[string]interface{}
    for rows.Next() {
        var from, to string
        var fee, amt int64
        var ts time.Time
        rows.Scan(&from, &to, &fee, &amt, &ts)
        txs = append(txs, map[string]interface{}{
            "from": from, "to": to, "fee": fee, "total": amt, "time": ts.Format("15:04:05"),
        })
    }
    json.NewEncoder(w).Encode(txs)
}

func adminAuth(next http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        u, p, ok := r.BasicAuth()
        if !ok || u != adminUser || p != adminPass {
            http.Error(w, "UNAUTHORIZED", 401)
            return
        }
        next(w, r)
    }
}

// --- Frontend ---
func indexHandler(w http.ResponseWriter, r *http.Request) {
    html := `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>LUXCOMPUTE | A2A MARKETPLACE</title>
    <script src="https://cdn.tailwindcss.com"></script>
    <script src="https://cdn.ethers.io/lib/ethers-5.2.umd.min.js"></script>
    <style>
        :root { --bg: #050505; --panel: #0a0a0a; --border: #333; --text: #ccc; --alert: #ff3333; --safe: #00ff00; }
        body { font-family: 'Courier New', Courier, monospace; background: var(--bg); color: var(--text); background-size: cover; background-image: url('/background.png'); }
        .box { background: rgba(10,10,10,0.95); border: 1px solid var(--border); }
        .border-r { border-right: 1px solid var(--border); }
        .border-b { border-bottom: 1px solid var(--border); }
        .text-terminal { text-transform: uppercase; letter-spacing: 1px; }
        .status-light { width: 8px; height: 8px; display: inline-block; }
        .on { background: var(--safe); box-shadow: 0 0 5px var(--safe); }
        .off { background: var(--alert); }
        .btn-sys { border: 1px solid var(--text); color: var(--text); background: transparent; text-transform: uppercase; cursor: pointer; transition: 0.2s; }
        .btn-sys:hover { background: var(--text); color: black; }
        .btn-danger { border-color: var(--alert); color: var(--alert); }
        .btn-danger:hover { background: var(--alert); color: white; }
        .scanlines { position: fixed; top: 0; left: 0; width: 100%; height: 100%; background: linear-gradient(to bottom, rgba(255,255,255,0), rgba(255,255,255,0) 50%, rgba(0,0,0,0.2) 50%, rgba(0,0,0,0.2)); background-size: 100% 4px; pointer-events: none; z-index: 50; }
    </style>
</head>
<body>
    <div class="scanlines"></div>

    <!-- LOGIN -->
    <div id="login" class="h-screen flex flex-col items-center justify-center relative z-10">
        <div class="box p-10 w-96 text-center border-l-4 border-l-green-600">
            <img src="/logo.png" class="h-20 mx-auto mb-6 opacity-70 grayscale">
            <h1 class="text-2xl font-bold mb-8 tracking-widest">LUX<span class="text-white">COMPUTE</span> A2A</h1>
            <button onclick="connect()" class="btn-sys w-full py-4 text-xl font-bold">AUTHENTICATE WALLET</button>
        </div>
    </div>

    <!-- A2A TERMINAL -->
    <div id="term" class="hidden min-h-screen relative z-10 flex flex-col">
        <div class="h-16 box flex items-center justify-between px-6 border-b-2 border-b-gray-700">
            <div class="flex items-center gap-4">
                <img src="/logo.png" class="h-8 opacity-50">
                <div><span class="text-xs text-gray-500">SYSTEM: ONLINE</span><br><span class="text-sm font-bold">PEER-TO-PEER NODE</span></div>
            </div>
            <div class="text-right">
                <span class="text-xs text-gray-500">IDENTITY</span><br>
                <span class="text-sm font-mono text-green-500" id="wallet-addr">CONNECTING...</span>
            </div>
        </div>

        <div class="flex-1 p-4 grid grid-cols-12 gap-4 overflow-hidden">
            <!-- Left: User Info -->
            <div class="col-span-3 box p-4 flex flex-col gap-4">
                <div>
                    <span class="text-xs text-gray-500">AVAILABLE CREDITS (WEI)</span>
                    <span class="block text-3xl font-bold text-white" id="bal">0</span>
                </div>
                <div class="h-px bg-gray-800 my-2"></div>
                <div class="text-xs text-gray-500 mb-2">DEPOSIT FUNDS</div>
                <div class="p-2 bg-black border border-gray-800 text-[10px] text-gray-400 font-mono break-all">` + ownerWallet + `</div>
                <button onclick="refresh()" class="btn-sys w-full py-2 text-xs mt-2">SYNC BALANCE</button>
            </div>

            <!-- Center: GPU Marketplace -->
            <div class="col-span-6 box flex flex-col">
                <div class="p-3 bg-gray-900 border-b border-gray-800 text-xs text-gray-500 flex justify-between">
                    <span>RESOURCE ALLOCATION GRID</span>
                    <span class="animate-pulse text-green-500">LIVE FEED</span>
                </div>
                <div class="flex-1 overflow-y-auto p-2 grid grid-cols-2 gap-3" id="market-grid">
                    <!-- Providers injected via JS -->
                </div>
            </div>

            <!-- Right: Logs -->
            <div class="col-span-3 box flex flex-col">
                <div class="p-3 bg-gray-900 border-b border-gray-800 text-xs text-gray-500">A2A ACTIVITY LOG</div>
                <div class="flex-1 overflow-y-auto p-2 font-mono text-xs text-gray-400 space-y-2" id="logs"></div>
            </div>
        </div>
    </div>

    <!-- ADMIN SURV -->
    <div id="admin" class="hidden fixed inset-0 bg-black z-50 p-4 flex flex-col">
        <div class="flex justify-between items-center border-b border-red-900 pb-4 mb-4">
            <h1 class="text-xl font-bold text-red-500 tracking-[0.3em]">SURVEILLANCE // A2A TRANSACTIONS</h1>
            <button onclick="closeAdmin()" class="text-xs text-gray-600 hover:text-white">[ CLOSE ]</button>
        </div>
        <div class="grid grid-cols-3 gap-4 mb-4">
            <div class="box p-4"><span class="text-xs text-gray-500">AGENTS</span><div class="text-xl" id="adm-agents">0</div></div>
            <div class="box p-4"><span class="text-xs text-gray-500">NODES</span><div class="text-xl" id="adm-nodes">0</div></div>
            <div class="box p-4"><span class="text-xs text-gray-500">TOTAL FEES (WEI)</span><div class="text-xl" id="adm-rev">0</div></div>
        </div>
        <div class="box flex-1 overflow-y-auto p-4 font-mono text-xs">
            <table class="w-full text-left text-gray-400">
                <thead class="text-gray-600 border-b border-gray-800"><tr><th class="pb-2">TIME</th><th>FROM</th><th>TO</th><th>FEE</th></tr></thead>
                <tbody id="adm-tx-list"></tbody>
            </table>
        </div>
    </div>

    <div onclick="loginAdmin()" style="position:fixed; bottom:5px; right:5px; width:8px; height:8px; background:#222; cursor:pointer; border:1px solid #444;"></div>

    <script>
        let wallet = null;

        async function connect() {
            if(window.ethereum) {
                const acc = await window.ethereum.request({ method: 'eth_requestAccounts' });
                wallet = acc[0];
                document.getElementById('wallet-addr').innerText = wallet.substring(0,6)+"..."+wallet.substring(38);
                document.getElementById('login').classList.add('hidden');
                document.getElementById('term').classList.remove('hidden');
                refresh();
                loadMarketplace();
            }
        }

        async function refresh() {
            const r = await fetch('/api/balance?addr=' + wallet);
            const d = await r.json();
            document.getElementById('bal').innerText = d.balance || 0;
        }

        async function loadMarketplace() {
            const r = await fetch('/api/providers');
            const list = await r.json();
            const grid = document.getElementById('market-grid');
            grid.innerHTML = "";
            list.forEach(p => {
                grid.innerHTML += `
                <div class="box p-4 border hover:border-green-500 transition cursor-pointer group">
                    <div class="flex justify-between items-start mb-2">
                        <span class="text-xs font-bold text-green-500">${p.gpu}</span>
                        <span class="text-[10px] text-gray-500 bg-black px-1">${p.id}</span>
                    </div>
                    <div class="text-xs text-gray-400 mb-4">OWNER: ${p.wallet.substring(0,10)}...</div>
                    <div class="flex justify-between items-center">
                        <span class="text-xs font-mono text-white">${p.price} WEI</span>
                        <button onclick="rent('${p.id}', '${p.wallet}', ${p.price})" class="btn-sys text-[10px] py-1 px-2 group-hover:bg-green-500 group-hover:text-black">RENT</button>
                    </div>
                </div>`;
            });
        }

        async function rent(pid, pw, price) {
            if(!confirm("CONFIRM RENTAL? COST: " + price + " WEI")) return;
            await fetch('/api/rent', {
                method: 'POST',
                body: JSON.stringify({ renter_wallet: wallet, provider_id: pid, provider_wallet: pw })
            });
            log("RENTED " + pid + " // DEDUCTED " + price + " WEI");
            refresh();
        }

        function log(msg) { document.getElementById('logs').innerHTML = '<div class="text-green-500">> ' + msg + '</div>' + document.getElementById('logs').innerHTML; }

        // ADMIN
        function loginAdmin() {
            const u = prompt("ID"); const p = prompt("PWD");
            if(u === "` + adminUser + `" && p === "` + adminPass + `") {
                document.getElementById('admin').classList.remove('hidden');
                pollAdmin();
            }
        }
        function closeAdmin() { document.getElementById('admin').classList.add('hidden'); }

        async function pollAdmin() {
            fetch('/api/admin/overview', {headers:{'Authorization':'Basic ' + btoa('` + adminUser + `:` + adminPass + `')}}).then(r=>r.json()).then(d=>{
                document.getElementById('adm-agents').innerText = d.agents;
                document.getElementById('adm-nodes').innerText = d.nodes;
                document.getElementById('adm-rev').innerText = d.revenue;
            });
            fetch('/api/admin/a2a-tx', {headers:{'Authorization':'Basic ' + btoa('` + adminUser + `:` + adminPass + `')}}).then(r=>r.json()).then(txs=>{
                const tb = document.getElementById('adm-tx-list'); tb.innerHTML = "";
                txs.forEach(t => {
                    tb.innerHTML += `<tr class="border-b border-gray-800"><td class="py-1">${t.time}</td><td>${t.from.substring(0,8)}...</td><td>${t.to.substring(0,8)}...</td><td class="text-red-500">${t.fee}</td></tr>`;
                });
            });
            setTimeout(pollAdmin, 3000);
        }
    </script>
</body>
</html>`
    w.Header().Set("Content-Type", "text/html")
    w.Write([]byte(html))





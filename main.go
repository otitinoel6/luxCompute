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
    _ "github.com/lib/pq"
    "github.com/ethereum/go-ethereum/core/types"
    "github.com/ethereum/go-ethereum/ethclient"
    "time"
)

var (
    db          *sql.DB
    dbUrl       = os.Getenv("DATABASE_URL")
    adminUser   = os.Getenv("ADMIN_USER")
    adminPass   = os.Getenv("ADMIN_PASS")
    ownerWallet = os.Getenv("OWNER_WALLET")
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

    // 3. Schema & Seed Data
    initSchema()
    seedProviders()

    // 4. Background Watcher
    go monitorBlockchain()

    // 5. Static Assets
    fs := http.FileServer(http.Dir("."))
    http.Handle("/", fs)
    http.Handle("/logo.png", fs)
    http.Handle("/background.png", fs)

    // 6. Routes
    http.HandleFunc("/api/providers", getProviders)
    http.HandleFunc("/api/rent", handleRent)
    http.HandleFunc("/api/balance", checkRealBalance)
    http.HandleFunc("/api/admin/overview", adminAuth(handleAdminOverview))
    http.HandleFunc("/api/admin/a2a-tx", adminAuth(handleA2ATransactions))

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
        wallet TEXT UNIQUE,
        node_id TEXT,
        gpu_model TEXT,
        status TEXT DEFAULT 'online',
        price_wei BIGINT
    );
    CREATE TABLE IF NOT EXISTS a2a_jobs (
        id SERIAL PRIMARY KEY,
        from_wallet TEXT,
        to_wallet TEXT,
        fee_wei BIGINT,
        amount_wei BIGINT,
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

func seedProviders() {
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
        header, err := ethClient.HeaderByNumber(context.Background(), nil)
        if err != nil {
            time.Sleep(10 * time.Second)
            continue
        }
        current := header.Number.Int64()
        if lastBlock == 0 { lastBlock = current - 10 }
        
        for b := lastBlock + 1; b <= current; b++ {
            block, err := ethClient.BlockByNumber(context.Background(), big.NewInt(b))
            if err != nil {
                log.Printf("Error fetching block %d: %v", b, err)
                continue
            }
            for _, tx := range block.Transactions() {
                if tx.To() != nil && tx.To().Hex() == ownerWallet && tx.Value().Sign() > 0 {
                    // FIX: Use Envelope to robustly extract Sender (From) in modern Go-Ethereum
                    envelope, err := types.NewEnvelope(tx)
                    if err != nil {
                        // Skip if transaction is malformed/unsupported
                        continue
                    }
                    sender := envelope.From().Hex()
                    
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

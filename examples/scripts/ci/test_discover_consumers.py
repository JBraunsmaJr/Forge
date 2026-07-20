import pytest
from discover_consumers import detect_affected_consumers

def test_detect_affected_consumers():
    all_services = ["auth", "billing"]
    changed_paths = ["services/auth/main.go"]
    
    affected = detect_affected_consumers(all_services, changed_paths)
    assert affected == ["auth"]

def test_detect_affected_consumers_none():
    all_services = ["auth", "billing"]
    changed_paths = ["README.md"]
    
    affected = detect_affected_consumers(all_services, changed_paths)
    assert affected == []
